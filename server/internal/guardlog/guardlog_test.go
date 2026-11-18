package guardlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const canary = "CANARYguardlog"

var plus2 = time.FixedZone("plus2", 2*3600)

func entry(ts, tool, d string, ms int, sub bool, extra string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"tool":%q,"eng":"go","d":%q,"ms":%d,"sub":%t%s}`+"\n", ts, tool, d, ms, sub, extra)
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir string, tracked []File) Pass {
	t.Helper()
	p, err := Reader{Dir: dir, Loc: plus2}.Read(tracked)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return p
}

func calls(p Pass) int64 {
	var n int64
	for _, c := range p.Hours {
		n += c.Calls
	}
	return n
}

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

// dump renders everything a pass keeps, for the no-text checks.
func dump(p Pass) string {
	var b strings.Builder
	for k, c := range p.Hours {
		fmt.Fprintf(&b, "%+v %+v\n", k, *c)
	}
	for k, n := range p.Latency {
		fmt.Fprintf(&b, "%+v %d\n", k, n)
	}
	for k, n := range p.Rules {
		fmt.Fprintf(&b, "%+v %d\n", k, n)
	}
	if p.Deny != nil {
		fmt.Fprintf(&b, "%+v\n", *p.Deny)
	}
	fmt.Fprintf(&b, "%+v %+v\n", p.Files, p.Skipped)
	return b.String()
}

func TestCountsPerLocalDayHourDecisionFlagAndRule(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "guard.log"),
		entry("2026-11-15T22:10:00.000Z", "bash", "allow", 3, false, ""),
		entry("2026-11-15T21:59:59.999Z", "file", "ask", 40, true, `,"rule":"file.outside-home"`),
		entry("2026-11-16T09:41:07.123Z", "bash", "deny", 120, true, `,"flags":["sub_deny"],"rule":"bash.rm-rf"`),
		entry("2026-11-16T09:42:00.000Z", "bash", "deny", 7, true, `,"flags":["sub_deny"],"rule":"bash.rm-rf"`),
		entry("2026-11-16T09:50:00.000Z", "bash", "deny", 600000, false, `,"flags":["time_cap","fail_closed"]`),
		entry("2026-11-16T09:30:00.000Z", "file", "deny", 2, false, `,"rule":"file.env"`))
	p := read(t, dir, nil)
	wantHours := map[HourKey]HourCount{
		{day("2026-11-16"), 0, "bash", "allow", false}: {Calls: 1},
		{day("2026-11-15"), 23, "file", "ask", true}:   {Calls: 1},
		{day("2026-11-16"), 11, "bash", "deny", true}:  {Calls: 2, SubDeny: 2},
		{day("2026-11-16"), 11, "bash", "deny", false}: {Calls: 1, TimeCaps: 1, FailClosed: 1},
		{day("2026-11-16"), 11, "file", "deny", false}: {Calls: 1},
	}
	got := map[HourKey]HourCount{}
	for k, c := range p.Hours {
		got[k] = *c
	}
	if !reflect.DeepEqual(got, wantHours) {
		t.Fatalf("hours\n got %v\nwant %v", got, wantHours)
	}
	wantLatency := map[LatencyKey]int64{
		{day("2026-11-16"), "bash", 3}: 1, {day("2026-11-15"), "file", 40}: 1, {day("2026-11-16"), "bash", 120}: 1,
		{day("2026-11-16"), "bash", 7}: 1, {day("2026-11-16"), "bash", 600000}: 1, {day("2026-11-16"), "file", 2}: 1,
	}
	if !reflect.DeepEqual(p.Latency, wantLatency) {
		t.Fatalf("latency %v", p.Latency)
	}
	wantRules := map[RuleKey]int64{
		{day("2026-11-15"), "file", "ask", "file.outside-home"}: 1, {day("2026-11-16"), "bash", "deny", "bash.rm-rf"}: 2,
		{day("2026-11-16"), "file", "deny", "file.env"}: 1,
	}
	if !reflect.DeepEqual(p.Rules, wantRules) {
		t.Fatalf("rules %v", p.Rules)
	}
	at, _ := time.Parse(time.RFC3339, "2026-11-16T09:50:00Z")
	if p.Deny == nil || *p.Deny != (Deny{At: at, Tool: "bash"}) {
		t.Fatalf("last deny %+v: the latest deny wins even when it is not the last line", p.Deny)
	}
	st, _ := os.Stat(filepath.Join(dir, "guard.log"))
	if p.Lines != 6 || p.Skipped != (Skipped{}) || !p.Present || p.Refused || len(p.Files) != 1 ||
		p.Files[0].Offset != st.Size() || len(p.Files[0].First) != 16 {
		t.Fatalf("pass %+v", p)
	}
	if again := read(t, dir, p.Files); calls(again) != 0 || again.Lines != 0 || !reflect.DeepEqual(again.Files, p.Files) {
		t.Fatalf("an unchanged log was counted again: %+v", again)
	}
}

func TestBadLinesAreCountedNeverKept(t *testing.T) {
	dir := t.TempDir()
	head := strings.TrimSuffix(entry("2026-11-16T10:00:00.000Z", "bash", "allow", 1, false, ""), "}\n") + `,"z":"`
	exact := head + strings.Repeat("x", MaxLine-len(head)-2) + `"}` + "\n"
	over := head + strings.Repeat("x", MaxLine-len(head)-1) + `"}` + "\n"
	if len(exact) != MaxLine+1 || len(over) != MaxLine+2 {
		t.Fatalf("fixture sizes %d %d", len(exact), len(over))
	}
	write(t, filepath.Join(dir, "guard.log"),
		"not json "+canary+"\n",
		"\n",
		`{"v":1}`+"\n",
		`["`+canary+`"]`+"\n",
		entry("2026-11-16T10:00:00.000Z", "bash", "allow", 1, false, ""),
		exact,
		over,
		strings.Replace(entry("2026-11-16T10:00:00.000Z", "bash", "allow", 1, false, ""), `"v":1`, `"v":2`, 1),
		entry("2026-11-16T10:00:00.000Z", "bash", "deny", 1, false, `,"flags":["`+canary+`"]`),
		entry("2026-11-16T10:00:00.000Z", "bash", "deny", 1, false, `,"rule":"Bad `+canary+`"`),
		entry("2026-11-16T10:00:00.000Z", "bash", "allow", 1, false, `,"rule":"bash.rm-rf"`),
		entry("2026-11-16T10:00:00.000Z", "file", "ask", 1, false, `,"rule":"`+strings.Repeat("a", 49)+`"`),
		entry("2026-11-16T10:00:00.000Z", "bash", "ask", 1, false, `,"cmd":"`+canary+`","nested":{"a":[1]}`),
		entry("2026-11-16T10:00:00.000Z", "bash", "ask", 600001, false, ""),
		entry("2026-11-16T10:00:00.000Z", "bash", "ask", -1, false, ""),
		strings.Replace(entry("2026-11-16T10:00:00.000Z", "bash", "ask", 1, false, ""), `"ms":1`, `"ms":1.5`, 1),
		entry("2026-11-16T10:00:00.000+01:00", "bash", "ask", 1, false, ""),
		entry("2026-11-16T10:00:00.000Z", "Bash", "ask", 1, false, ""),
		entry("2026-11-16T10:00:00.000Z", "bash", "block", 1, false, ""),
		strings.Replace(entry("2026-11-16T10:00:00.000Z", "bash", "ask", 1, false, ""), `"eng":"go"`, `"eng":"rust"`, 1),
		strings.Replace(entry("2026-11-16T10:00:00.000Z", "bash", "ask", 1, false, ""), `,"sub":false`, "", 1))
	p := read(t, dir, nil)
	want := Skipped{Malformed: 13, Oversized: 1, BadVersion: 1, BadRule: 3}
	if p.Skipped != want {
		t.Fatalf("skipped %+v, want %+v", p.Skipped, want)
	}
	if p.Lines != 21 || calls(p) != 6 {
		t.Fatalf("lines %d calls %d: valid, 512-byte, two bad rules, long rule and unknown keys count", p.Lines, calls(p))
	}
	if len(p.Rules) != 0 {
		t.Fatalf("a bad or misplaced rule kept its label: %v", p.Rules)
	}
	if p.Deny == nil || p.Deny.Rule != "" {
		t.Fatalf("deny with a bad rule: %+v", p.Deny)
	}
	if s := dump(p); strings.Contains(strings.ToLower(s), "canary") || strings.Contains(s, "xxxx") {
		t.Fatalf("line text kept: %s", s)
	}
}

func TestRotationWhileBothFilesGrowCountsEachLineOnce(t *testing.T) {
	dir := t.TempDir()
	cur, one, two := filepath.Join(dir, "guard.log"), filepath.Join(dir, "guard.log.1"), filepath.Join(dir, "guard.log.2")
	line := func(i int) string {
		return entry(fmt.Sprintf("2026-11-16T10:00:%02d.000Z", i), "bash", "allow", i, false, "")
	}
	write(t, cur, line(1), line(2), line(3))
	p := read(t, dir, nil)
	if calls(p) != 3 {
		t.Fatalf("first pass %d", calls(p))
	}
	w, err := os.OpenFile(cur, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	appendTo(t, cur, line(4))
	if err := os.Rename(cur, one); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(line(5)); err != nil {
		t.Fatal(err)
	}
	write(t, cur, line(6), line(7))
	p = read(t, dir, p.Files)
	if calls(p) != 4 || len(p.Files) != 2 {
		t.Fatalf("after rotation %d calls, files %+v; want lines 4-7 once", calls(p), p.Files)
	}
	if _, err := w.WriteString(line(8)); err != nil {
		t.Fatal(err)
	}
	appendTo(t, cur, line(9))
	if err := os.Rename(one, two); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cur, one); err != nil {
		t.Fatal(err)
	}
	write(t, cur, line(10))
	p = read(t, dir, p.Files)
	if calls(p) != 3 || len(p.Files) != 3 {
		t.Fatalf("second rotation %d calls, files %d; want lines 8-10 once", calls(p), len(p.Files))
	}
	if p = read(t, dir, p.Files); calls(p) != 0 {
		t.Fatalf("idle pass counted %d", calls(p))
	}
}

// A rotation between the reads of guard.log.1 and guard.log hides the old guard.log for one pass.
func TestAFileHiddenForOnePassResumesWhereItWas(t *testing.T) {
	dir, aside := t.TempDir(), t.TempDir()
	cur := filepath.Join(dir, "guard.log")
	write(t, cur, entry("2026-11-16T10:00:01.000Z", "bash", "allow", 1, false, ""))
	p := read(t, dir, nil)
	if err := os.Rename(cur, filepath.Join(aside, "guard.log")); err != nil {
		t.Fatal(err)
	}
	write(t, cur, entry("2026-11-16T10:00:02.000Z", "bash", "allow", 1, false, ""))
	p = read(t, dir, p.Files)
	if calls(p) != 1 || len(p.Files) != 2 {
		t.Fatalf("pass with the old file hidden: %d calls, files %+v", calls(p), p.Files)
	}
	if err := os.Rename(filepath.Join(aside, "guard.log"), filepath.Join(dir, "guard.log.1")); err != nil {
		t.Fatal(err)
	}
	if p = read(t, dir, p.Files); calls(p) != 0 {
		t.Fatalf("the file that came back was read again: %d calls", calls(p))
	}
}

func TestTruncationAndInodeReuseReadFromTheStart(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "guard.log")
	first := entry("2026-11-16T10:00:01.000Z", "bash", "allow", 1, false, "")
	write(t, cur, first, entry("2026-11-16T10:00:02.000Z", "bash", "allow", 1, false, ""))
	p := read(t, dir, nil)
	if err := os.Truncate(cur, 0); err != nil {
		t.Fatal(err)
	}
	appendTo(t, cur, first)
	p = read(t, dir, p.Files)
	if calls(p) != 1 || p.Skipped.Truncated != 1 {
		t.Fatalf("truncated, same first line: %d calls, %+v", calls(p), p.Skipped)
	}
	if err := os.Truncate(cur, 0); err != nil {
		t.Fatal(err)
	}
	appendTo(t, cur, entry("2026-11-16T11:00:00.000Z", "file", "ask", 1, false, ""),
		entry("2026-11-16T11:00:01.000Z", "file", "ask", 1, false, ""), first)
	p = read(t, dir, p.Files)
	if calls(p) != 3 || p.Skipped.Truncated != 0 {
		t.Fatalf("rewritten past the old offset with a new first line: %d calls, %+v", calls(p), p.Skipped)
	}
	reused := []File{{Dev: p.Files[0].Dev, Ino: p.Files[0].Ino, First: "0123456789abcdef", Offset: 10}}
	if p = read(t, dir, reused); calls(p) != 3 {
		t.Fatalf("same device and inode, other first line: %d calls, want the whole file", calls(p))
	}
}

func TestPartialLastLineWaitsForItsNewline(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "guard.log")
	whole := entry("2026-11-16T10:00:03.000Z", "file", "deny", 9, false, `,"rule":"file.env"`)
	write(t, cur, entry("2026-11-16T10:00:01.000Z", "bash", "allow", 1, false, ""), whole[:20])
	p := read(t, dir, nil)
	if calls(p) != 1 || p.Skipped != (Skipped{}) {
		t.Fatalf("first pass %d calls %+v", calls(p), p.Skipped)
	}
	appendTo(t, cur, whole[20:len(whole)-1])
	if q := read(t, dir, p.Files); calls(q) != 0 || q.Skipped != (Skipped{}) || q.Files[0].Offset != p.Files[0].Offset {
		t.Fatalf("a line without its newline was read: %+v", q)
	}
	appendTo(t, cur, "\n")
	p = read(t, dir, p.Files)
	if calls(p) != 1 || len(p.Rules) != 1 || p.Skipped != (Skipped{}) {
		t.Fatalf("completed line: %d calls %+v", calls(p), p.Skipped)
	}

	empty := t.TempDir()
	write(t, filepath.Join(empty, "guard.log"), whole[:20])
	p = read(t, empty, nil)
	appendTo(t, filepath.Join(empty, "guard.log"), whole[20:])
	if p = read(t, empty, p.Files); calls(p) != 1 {
		t.Fatalf("a first line completed later: %d calls", calls(p))
	}
}

func TestBudgetLeavesTheRestForTheNextPass(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, entry(fmt.Sprintf("2026-11-16T10:00:%02d.000Z", i), "bash", "allow", i, false, ""))
	}
	write(t, filepath.Join(dir, "guard.log.1"), lines[:5]...)
	write(t, filepath.Join(dir, "guard.log"), lines[5:]...)
	var files []File
	var total int64
	for pass := 0; pass < 20 && total < 10; pass++ {
		p, err := Reader{Dir: dir, Loc: plus2, Budget: int64(len(lines[0]) * 3)}.Read(files)
		if err != nil {
			t.Fatal(err)
		}
		if n := calls(p); n > 3 {
			t.Fatalf("pass %d read %d lines over its budget", pass, n)
		}
		total += calls(p)
		files = p.Files
	}
	if total != 10 {
		t.Fatalf("%d calls over all passes, want 10", total)
	}
}

func TestLinksAndNonRegularFilesAreNotRead(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	target := filepath.Join(other, "real.log")
	write(t, target, entry("2026-11-16T10:00:01.000Z", "bash", "deny", 1, false, ""))
	if err := os.Symlink(target, filepath.Join(dir, "guard.log")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "guard.log.1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "guard.log.2"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "guard.log.2"), filepath.Join(dir, "guard.log.3")); err != nil {
		t.Fatal(err)
	}
	done := make(chan Pass, 1)
	go func() { done <- read(t, dir, nil) }()
	var p Pass
	select {
	case p = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader blocked on a FIFO")
	}
	if calls(p) != 0 || p.Deny != nil || len(p.Files) != 0 || p.Skipped.NotRegular != 4 || !p.Present ||
		!p.Refused {
		t.Fatalf("pass %+v", p)
	}
}

func TestMissingLogIsNotPresent(t *testing.T) {
	p := read(t, filepath.Join(t.TempDir(), "absent"), nil)
	if p.Present || p.Refused || len(p.Files) != 0 || p.Lines != 0 || p.Deny != nil {
		t.Fatalf("pass %+v", p)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "guard.log.2"), entry("2026-11-16T10:00:01.000Z", "bash", "allow", 1, false, ""))
	if p = read(t, dir, nil); !p.Present || calls(p) != 1 || p.Refused {
		t.Fatalf("only a rotated file: %+v", p)
	}
}

func TestBudgetEndingMidLineNeverStallsOrRepeatsALine(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, entry(fmt.Sprintf("2026-11-16T10:00:%02d.000Z", i), "bash", "allow", 7, false, ""))
	}
	write(t, filepath.Join(dir, "guard.log"), lines...)
	budget := int64(len(lines[0])*3 + len(lines[0])/2)
	var files []File
	var seen []int64
	for pass := 0; pass < 6; pass++ {
		p, err := Reader{Dir: dir, Loc: plus2, Budget: budget}.Read(files)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, calls(p))
		files = p.Files
	}
	if !reflect.DeepEqual(seen, []int64{3, 3, 3, 1, 0, 0}) {
		t.Fatalf("calls per pass %v, want 3 3 3 1 0 0", seen)
	}
}

func TestUnterminatedRunLongerThanTheLimitIsOneOversizedLineAcrossPasses(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "guard.log")
	first := entry("2026-11-16T10:00:01.000Z", "bash", "allow", 1, false, "")
	write(t, log, first, strings.Repeat("x", 3000))
	var files []File
	var total int64
	var oversized, lines int64
	run := func(budget int64) Pass {
		t.Helper()
		p, err := Reader{Dir: dir, Loc: plus2, Budget: budget}.Read(files)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(p.Files)
		files = nil
		if err != nil || json.Unmarshal(raw, &files) != nil {
			t.Fatalf("positions do not survive storage: %v", err)
		}
		total += calls(p)
		oversized += p.Skipped.Oversized
		lines += p.Lines
		return p
	}
	for pass := 0; pass < 8; pass++ {
		run(1000)
	}
	if total != 1 || oversized != 1 || lines != 2 || !files[0].Skip {
		t.Fatalf("before the newline: %d calls, %d oversized, %d lines, positions %+v", total, oversized, lines, files)
	}
	appendTo(t, log, "tail of the long line\n", entry("2026-11-16T10:00:09.000Z", "file", "deny", 5, false, ""))
	for pass := 0; pass < 3; pass++ {
		run(1000)
	}
	if total != 2 || oversized != 1 || lines != 3 || files[0].Skip {
		t.Fatalf("after the newline: %d calls, %d oversized, %d lines, positions %+v", total, oversized, lines, files)
	}

	short := t.TempDir()
	write(t, filepath.Join(short, "guard.log"), strings.Repeat("y", MaxLine+1))
	if p := read(t, short, nil); p.Lines != 0 || p.Skipped != (Skipped{}) || p.Files[0].Offset != 0 {
		t.Fatalf("a tail of %d bytes may still be a valid line: %+v", MaxLine+1, p)
	}
	write(t, filepath.Join(short, "guard.log"), strings.Repeat("y", MaxLine+2))
	if p := read(t, short, nil); p.Lines != 1 || p.Skipped.Oversized != 1 || p.Files[0].Offset != MaxLine+2 {
		t.Fatalf("a run past the limit: %+v", p)
	}
}

func TestRefusedRotationIsReportedAndTheRegularFilesStillCount(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	write(t, filepath.Join(other, "real.log"), entry("2026-11-16T10:00:01.000Z", "bash", "allow", 1, false, ""))
	write(t, filepath.Join(dir, "guard.log"), entry("2026-11-16T10:00:02.000Z", "bash", "allow", 1, false, ""))
	if err := os.Symlink(filepath.Join(other, "real.log"), filepath.Join(dir, "guard.log.1")); err != nil {
		t.Fatal(err)
	}
	p := read(t, dir, nil)
	if !p.Refused || p.Skipped.NotRegular != 1 || calls(p) != 1 || len(p.Files) != 1 {
		t.Fatalf("symlinked guard.log.1: %+v", p)
	}
	if err := os.Remove(filepath.Join(dir, "guard.log.1")); err != nil {
		t.Fatal(err)
	}
	if p = read(t, dir, p.Files); p.Refused || p.Skipped.NotRegular != 0 {
		t.Fatalf("after the link is gone: %+v", p)
	}
	if err := os.Link(filepath.Join(dir, "guard.log"), filepath.Join(dir, "guard.log.2")); err != nil {
		t.Fatal(err)
	}
	if p = read(t, dir, nil); p.Refused || p.Skipped.NotRegular != 0 || calls(p) != 1 {
		t.Fatalf("a second name of the same file is not a refusal: %+v", p)
	}
}
