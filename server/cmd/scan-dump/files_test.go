package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"benchwatch/server/internal/scan"
)

func unreadableSupported(t *testing.T, b *box) bool {
	probe := filepath.Join(b.dir, "probe")
	must(t, os.WriteFile(probe, []byte("x"), 0o600))
	must(t, os.Chmod(probe, 0))
	_, err := os.ReadFile(probe)
	_ = os.Remove(probe)
	return err != nil
}

func mode(t *testing.T, file string) os.FileMode {
	t.Helper()
	st, err := os.Stat(file)
	must(t, err)
	return st.Mode().Perm()
}

func TestTheStatusIsPrivateAndHoldsThreeKeys(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	s := b.scan()
	fatalf(t, reflect.DeepEqual(statusKeys(s.status), []string{"code", "lastRunAt", "ok"}), "keys %v", s.status)
	equal(t, mode(t, filepath.Join(b.cache, "status.json")), 0o600, "status mode")
}

func TestPathsResolveAgainstWhereTheDumpStarted(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	t.Chdir(b.dir)
	o, err := parse([]string{"-projects", "projects", "-quiet", "out/data.json", "cache"})
	must(t, err)
	must(t, runAndRecord(o))
	equal(t, data(readJSON(t, b.out)).stat("uniqueCalls"), 1, "calls")
	_, err = os.Stat(filepath.Join(b.cache, "status.json"))
	must(t, err)
}

func TestTheDumpRefusesToRunWithoutAWebFolder(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	t.Setenv("BENCHWATCH_WEB_DIR", "")
	equal(t, codeOf(b.runWith(func(*options) {}).err), "EWEBDIR", "no web folder named")
}

func TestTheDumpRefusesARelativeWebFolder(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	t.Setenv("BENCHWATCH_WEB_DIR", "web")
	equal(t, codeOf(b.runWith(func(*options) {}).err), "EWEBDIR", "relative web folder")
}

func TestTheDumpRefusesTheLiveCacheTheWebFolderAndTheTranscripts(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	home := filepath.Join(b.dir, "home")
	must(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)
	live := filepath.Join(home, "Library/Application Support/benchwatch")
	web := filepath.Join(home, "site")
	t.Setenv("BENCHWATCH_WEB_DIR", web)
	must(t, os.MkdirAll(filepath.Dir(live), 0o755))
	link := filepath.Join(b.dir, "link")
	must(t, os.Symlink(filepath.Dir(live), link))
	cases := []struct {
		name string
		set  func(*options)
	}{
		{"live cache", func(o *options) { o.cache = filepath.Join(live, "cache") }},
		{"live output", func(o *options) { o.out = filepath.Join(live, "public/data.json") }},
		{"live, other case",
			func(o *options) { o.cache = filepath.Join(home, "Library/application support/BenchWatch/cache") }},
		{"live through a link", func(o *options) { o.cache = filepath.Join(link, "benchwatch/cache") }},
		{"web folder", func(o *options) { o.out = filepath.Join(web, "public/data.json") }},
		{"inside the transcripts", func(o *options) { o.cache = filepath.Join(b.root, "cache") }},
		{"above the transcripts", func(o *options) { o.cache = b.dir }},
	}
	for _, c := range cases {
		r := b.runWith(c.set)
		equal(t, codeOf(r.err), "EREFUSED", c.name)
	}
	for _, dir := range []string{live, web, filepath.Join(b.root, "cache")} {
		_, err := os.Lstat(dir)
		fatalf(t, os.IsNotExist(err), "a refused run created a folder")
	}
}

func TestATranscriptWhoseShardCannotBeCommittedCountsAsUnreadable(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.write(otherRel, call("o1", 2, nil))
	must(t, os.MkdirAll(b.shardFile(mainRel), 0o700))
	must(t, os.WriteFile(filepath.Join(b.shardFile(mainRel), "keep"), nil, 0o600))
	s := b.scan()
	equal(t, s.status["ok"], any(true), "ok")
	equal(t, s.data.stat("unreadable"), 1, "unreadable")
	equal(t, total(s.data.rows("usage"), 4), 1, "the healthy transcript is there")
	must(t, os.RemoveAll(b.shardFile(mainRel)))
	equal(t, total(b.scan().data.rows("usage"), 4), 2, "recovered once the shard can be written")
}

func TestALineAboveTheCapIsSkippedWithoutBufferingIt(t *testing.T) {
	b := sandbox(t)
	file := b.write(mainRel, call("before", 1, nil))
	must(t, os.Truncate(file, 300<<20))
	appendTo(t, file, "", call("after", 2, nil))
	c := b.runChild()
	equal(t, c.exit, 0, "exit")
	fatalf(t, c.maxRSS < 200e6, "peak RSS %d MB", c.maxRSS/1e6)
	d := data(readJSON(t, b.out))
	equal(t, total(d.rows("usage"), 4), 2, "calls")
	equal(t, d.stat("skipped"), 1, "skipped")
	equal(t, d.stat("lines"), 3, "lines")
	appendTo(t, file, call("resumed", 3, nil))
	d = b.scan().data
	equal(t, total(d.rows("usage"), 4), 3, "the offset resumed at the next newline")
	equal(t, d.stat("skipped"), 1, "skipped")
}

func paddedCall(t *testing.T, id string, min, size int) []byte {
	t.Helper()
	line := encodeLine(with(call(id, min, nil), rec{"pad": ""}))
	padded := encodeLine(with(call(id, min, nil), rec{"pad": strings.Repeat("a", size-len(line))}))
	equal(t, len(padded), size, "padded line length")
	return append(padded, '\n')
}

func TestAValid9MiBCallLineIsSkippedAndCounted(t *testing.T) {
	b := sandbox(t)
	file := b.write(mainRel, call("before", 1, nil))
	appendBytes(t, file, paddedCall(t, "big", 2, 9<<20))
	appendTo(t, file, call("after", 3, nil))
	s := b.scan()
	equal(t, s.data.stat("uniqueCalls"), 2, "calls around the long line")
	equal(t, s.data.stat("skipped"), 1, "skipped")
	equal(t, s.data.stat("lines"), 3, "lines")
	equal(t, s.status["ok"], any(true), "status")
}

func TestALineOfExactlyTheCapIsReadAndOneByteMoreIsSkipped(t *testing.T) {
	b := sandbox(t)
	file := b.write(mainRel, call("before", 1, nil))
	appendBytes(t, file, paddedCall(t, "at-cap", 2, scan.MaxLineBytes))
	appendBytes(t, file, paddedCall(t, "over-cap", 3, scan.MaxLineBytes+1))
	s := b.scan()
	equal(t, s.data.stat("uniqueCalls"), 2, "calls")
	equal(t, s.data.stat("skipped"), 1, "skipped")
}

func TestAnUnfinishedOversizedLastLineWaitsForItsNewline(t *testing.T) {
	b := sandbox(t)
	file := b.write(mainRel, call("m1", 1, nil))
	must(t, os.Truncate(file, 200<<20))
	c := b.runChild()
	equal(t, c.exit, 0, "exit")
	fatalf(t, c.maxRSS < 150e6, "peak RSS %d MB", c.maxRSS/1e6)
	first := data(readJSON(t, b.out))
	equal(t, first.stat("lines"), 1, "lines")
	equal(t, first.stat("skipped"), 0, "skipped")
	appendTo(t, file, "", call("m2", 2, nil))
	d := b.scan().data
	equal(t, total(d.rows("usage"), 4), 2, "calls")
	equal(t, d.stat("lines"), 3, "lines")
	equal(t, d.stat("skipped"), 1, "skipped")
}

func TestEventsPerShardAreCapped(t *testing.T) {
	b := sandbox(t)
	var lines []any
	for line := 0; line < 7; line++ {
		ids := make([]any, 100_000)
		for i := range ids {
			ids[i] = rec{"type": "tool_result", "tool_use_id": fmt.Sprintf("%d-%d", line, i), "content": ""}
		}
		lines = append(lines, rec{"type": "user", "timestamp": T(1), "message": rec{"content": ids}})
	}
	b.write(mainRel, lines...)
	b.write(otherRel, call("o1", 2, nil))
	s := b.scan()
	equal(t, s.status["ok"], any(true), "ok")
	equal(t, total(s.data.rows("usage"), 4), 1, "calls")
	equal(t, s.data.stat("skipped"), 2, "skipped")
	equal(t, len(shardEvents(t, b.shardFile(mainRel))), 500_000, "events")
}

func TestLabelsPerSegmentAreCapped(t *testing.T) {
	b := sandbox(t)
	var uses []any
	var results []any
	for i := 0; i < 300; i++ {
		name := "mcp__srv__tool_" + word(i+26)
		uses = append(uses, toolUse(fmt.Sprintf("u%d", i), name, rec{}))
		results = append(results, toolResult(fmt.Sprintf("u%d", i), 2, strings.Repeat("x", 40), nil))
	}
	records := append([]any{call("m1", 1, nil, uses...)}, results...)
	b.write(mainRel, append(records, call("m2", 3, nil), call("m3", 4, nil))...)
	d := b.scan().data
	rereads := where(d.rows("fills"), func(r []float64) bool { return r[6] > 0 })
	equal(t, len(rereads), 257, "256 labels plus other")
	equal(t, total(d.rows("fills"), 6), 300*10*2, "each result is re-read by both later calls")
	equal(t, len(where(d.rows("fills"), func(r []float64) bool { return r[4] > 0 })), 300,
		"first-seen results keep their own label")
	other := where(rereads, func(r []float64) bool { return d.name("labels", r[3]) == "other" })
	equal(t, other[0][6], 44*10*2, "other")
}

func TestAbsurdTimestampsAreClamped(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel,
		with(call("far-future", 1, nil), rec{"timestamp": "+275760-09-13T00:00:00.000Z"}),
		with(call("far-past", 2, nil), rec{"timestamp": "-271821-04-20T00:00:00.000Z"}),
		call("normal", 3, nil))
	d := b.scan().data
	day := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	for _, label := range d.strings("days") {
		fatalf(t, day.MatchString(label), "day %q", label)
	}
	equal(t, total(d.rows("usage"), 4), 3, "calls")
}

func TestACorruptShardThatCannotBeRebuiltIsRetried(t *testing.T) {
	b := sandbox(t)
	if !unreadableSupported(t, b) {
		t.Skip("this user can read a mode 000 file")
	}
	mainFile := b.write(mainRel, call("m1", 1, nil), call("m2", 2, nil))
	b.write(otherRel, call("o1", 3, nil))
	expected := b.scan().data
	must(t, os.WriteFile(b.shardFile(mainRel), []byte("junk"), 0o600))
	must(t, os.Chmod(mainFile, 0))
	func() {
		defer func() { must(t, os.Chmod(mainFile, 0o644)) }()
		s := b.scan()
		equal(t, s.data.stat("unrebuilt"), 1, "unrebuilt")
		equal(t, s.data.stat("transcripts"), 1, "transcripts")
		equal(t, s.status["ok"], any(true), "ok")
		equal(t, b.scan().data.stat("unrebuilt"), 1, "still counted on the next run")
	}()
	sameTables(t, b.scan().data, expected, "recovered")
}

func TestFIFOsAndSymlinksDoNotHangOrRedirectTheScan(t *testing.T) {
	b := sandbox(t)
	secret := filepath.Join(b.dir, "outside.json")
	must(t, os.WriteFile(secret, []byte(`{"agentType":"from-a-symlink"}`), 0o600))
	agent := func(n int) string {
		return fmt.Sprintf("-Users-x-workspace-proj/aaaaaaaa-0000-0000-0000-000000000000/subagents/agent-%d.jsonl", n)
	}
	for n := 1; n <= 4; n++ {
		b.write(agent(n), call(fmt.Sprintf("s%d", n), n, nil))
	}
	meta := func(n int) string { return filepath.Join(b.root, strings.TrimSuffix(agent(n), ".jsonl")+".meta.json") }
	must(t, syscall.Mkfifo(meta(1), 0o600))
	must(t, os.Symlink(secret, meta(2)))
	must(t, os.WriteFile(meta(3), []byte(`{"agentType":"real-agent"}`), 0o600))
	must(t, os.WriteFile(meta(4), []byte(strings.Repeat("x", 2<<20)), 0o600))
	must(t, syscall.Mkfifo(filepath.Join(b.root, "-Users-x-workspace-proj/fifo.jsonl"), 0o600))
	must(t, os.Symlink(secret, filepath.Join(b.root, "-Users-x-workspace-proj/link.jsonl")))
	d := b.scan().data
	equal(t, total(d.rows("usage"), 4), 4, "calls")
	types := d.strings("agentTypes")
	sort.Strings(types)
	fatalf(t, reflect.DeepEqual(types, []string{"real-agent", "unknown"}), "agent types %v", types)
	equal(t, d.stat("transcripts"), 4, "the FIFO and the symlink named .jsonl are not transcripts")
}

func TestAnUnreadableFileOrFolderKeepsThePreviousShardUntilItIsGone(t *testing.T) {
	b := sandbox(t)
	if !unreadableSupported(t, b) {
		t.Skip("this user can read a mode 000 file")
	}
	known := b.write(mainRel, call("m1", 1, nil))
	b.write(otherRel, call("o1", 2, nil))
	folder := filepath.Dir(b.write("-Users-x-workspace-p2/cccccccc-0000-0000-0000-000000000000.jsonl", call("c1", 3, nil)))
	equal(t, b.scan().data.stat("unreadable"), 0, "unreadable")
	appendTo(t, known, call("m2", 4, nil))
	must(t, os.Chmod(known, 0))
	fresh := b.write(strings.Replace(otherRel, "bbbbbbbb", "dddddddd", 1), call("d1", 5, nil))
	must(t, os.Chmod(fresh, 0))
	must(t, os.Chmod(folder, 0))
	func() {
		defer func() {
			must(t, os.Chmod(known, 0o644))
			must(t, os.Chmod(fresh, 0o644))
			must(t, os.Chmod(folder, 0o755))
		}()
		s := b.scan()
		equal(t, s.status["ok"], any(true), "ok")
		equal(t, s.data.stat("unreadable"), 3, "unreadable")
		equal(t, s.data.stat("transcripts"), 3, "known, other and the file in the unreadable folder")
		equal(t, total(s.data.rows("usage"), 4), 3, "the previous shards are kept; the new file adds nothing yet")
		fatalf(t, strings.HasPrefix(b.scan().stdout, "no transcript changed"), "a stable count does not rewrite the output")
	}()
	d := b.scan().data
	equal(t, d.stat("unreadable"), 0, "unreadable")
	equal(t, total(d.rows("usage"), 4), 5, "resumed once readable")
	must(t, os.Remove(known))
	equal(t, total(b.scan().data.rows("usage"), 4), 3, "only ENOENT drops a transcript")
}

func TestTheDumpReplacesAPlantedFileInsteadOfFollowingIt(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.scan()
	victim := filepath.Join(b.dir, "victim.txt")
	must(t, os.WriteFile(victim, []byte("keep me"), 0o600))
	must(t, os.Remove(b.out))
	must(t, os.Symlink(victim, b.out))
	b.scan()
	text, _ := os.ReadFile(victim)
	equal(t, string(text), "keep me", "victim")
	st, err := os.Lstat(b.out)
	must(t, err)
	fatalf(t, st.Mode().IsRegular(), "the link was followed")

	must(t, os.Remove(b.out))
	must(t, syscall.Mkfifo(b.out, 0o600))
	must(t, b.runWith(nil).err)
	st, err = os.Lstat(b.out)
	must(t, err)
	fatalf(t, st.Mode().IsRegular(), "a FIFO in place of the dump must not hang the check")

	stale := b.out + ".0123456789abcdef.tmp"
	must(t, os.WriteFile(stale, []byte("left by a crash"), 0o600))
	old := time.Now().Add(-2 * time.Minute)
	must(t, os.Chtimes(stale, old, old))
	b.scan()
	_, err = os.Stat(stale)
	fatalf(t, os.IsNotExist(err), "a stale temp file was not swept")
	entries, _ := os.ReadDir(filepath.Dir(b.out))
	for _, e := range entries {
		fatalf(t, !strings.HasSuffix(e.Name(), ".tmp"), "a temp file is left")
	}
}

func TestTheCacheIsPrivate(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	must(t, os.MkdirAll(b.cache, 0o755))
	must(t, os.Chmod(b.cache, 0o755))
	b.scan()
	equal(t, mode(t, b.cache), 0o700, "cache mode")
	equal(t, mode(t, filepath.Join(b.cache, "shards")), 0o700, "shards mode")
	for _, f := range []string{b.shardFile(mainRel), b.indexFile(), scan.OutputStateFile(b.cache),
		filepath.Join(b.cache, "status.json")} {
		equal(t, mode(t, f), 0o600, filepath.Base(f))
	}
}

func TestAnIdleRescanReadsNoShardAndStaysUnderTheCPUBudget(t *testing.T) {
	b := sandbox(t)
	for f := 0; f < 400; f++ {
		var records []any
		for i := 0; i < 25; i++ {
			id := fmt.Sprintf("m%d-%d", f, i)
			records = append(records, call(id, i, nil, toolUse("u"+id, "Bash", rec{"command": "git status"})),
				toolResult("u"+id, i, strings.Repeat("x", 400), nil))
		}
		b.write(fmt.Sprintf("-Users-x-workspace-p%d/%08x-0000-4000-8000-000000000000.jsonl", f%7, f), records...)
	}
	first := b.runChild()
	equal(t, first.exit, 0, "first run")
	idle := b.runChild()
	equal(t, idle.exit, 0, "idle run")
	fatalf(t, strings.HasPrefix(idle.stdout, "no transcript changed (400 checked") &&
		strings.Contains(idle.stdout, " 0 shard reads"),
		"stdout %q", idle.stdout)
	fatalf(t, idle.cpu < 200*time.Millisecond, "idle CPU %v", idle.cpu)
	t.Logf("idle: %v CPU, %d MB RSS; full: %v CPU, %d MB RSS", idle.cpu, idle.maxRSS/1e6, first.cpu, first.maxRSS/1e6)
	store, err := scan.UpdateShards(b.root, b.cache, nil)
	must(t, err)
	equal(t, store.ShardReads, 0, "shard reads")
	equal(t, store.Changed, 0, "changed")
}
