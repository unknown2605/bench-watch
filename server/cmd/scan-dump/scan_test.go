package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	bwsource "benchwatch/server/internal/source"
)

func TestDedupesByMessageIDKeepingTheLargestUsage(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel,
		call("m1", 1, rec{"output_tokens": 3, "cache_read_input_tokens": 100}),
		call("m1", 1, rec{"output_tokens": 50, "cache_read_input_tokens": 100}),
		call("m2", 2, rec{"cache_read_input_tokens": 200}))
	b.write(subRel, call("m1", 1, rec{"output_tokens": 50, "cache_read_input_tokens": 100}),
		call("s1", 3, rec{"cache_read_input_tokens": 300}))
	d := b.scan().data
	usage := d.rows("usage")
	equal(t, total(usage, 4), 3, "calls")
	equal(t, total(usage, 7), 600, "cache read")
	equal(t, total(usage, 8), 52, "output")
	equal(t, total(where(usage, func(r []float64) bool { return r[2] == 0 }), 4), 2, "the first transcript owns the shared call")
	equal(t, d.stat("duplicateLinesDropped"), 1, "duplicates")
}

func TestTheEarlierBornTranscriptOwnsACallWhenFirstTimestampsTie(t *testing.T) {
	b := sandbox(t)
	original := "-Users-x-workspace-proj/ffffffff-0000-4000-8000-000000000000.jsonl"
	key := func(rel string) string { return bwsource.Key(clients.ClaudeCode, rel) }
	copied := ""
	for n := 0; n < 200 && copied == ""; n++ {
		candidate := fmt.Sprintf("-Users-x-workspace-proj/%08d-0000-4000-8000-000000000000.jsonl", n)
		if key(candidate) < key(original) {
			copied = candidate
		}
	}
	fatalf(t, copied != "" && copied < original, "the copy sorts first by path and by key")
	shared := []any{call("m1", 1, rec{"input_tokens": 10}), call("m2", 2, rec{"input_tokens": 20})}
	b.write(original, shared...)
	time.Sleep(20 * time.Millisecond)
	b.write(copied, append(shared, call("m3", 3, rec{"input_tokens": 30}))...)
	d := b.scan().data
	files := readJSON(t, b.indexFile())["files"].(map[string]any)
	birth := func(rel string) float64 { return files[key(rel)].(map[string]any)["birthMs"].(float64) }
	fatalf(t, birth(original) < birth(copied), "the original was born first")
	owned := map[string]float64{}
	for _, r := range d.rows("sessionRows") {
		owned[d.session(r[1])] = r[4]
	}
	want := map[string]float64{"ffffffff": 2, copied[24:32]: 1}
	fatalf(t, reflect.DeepEqual(owned, want), "owned %v", owned)
}

func TestSyntheticMessagesAreNotAPICalls(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil), withModel(call("syn", 2, nil), "<synthetic>"))
	equal(t, total(b.scan().data.rows("usage"), 4), 1, "calls")
}

func TestWeightedSizeResetsAtCompactBoundary(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel,
		call("m1", 1, nil, toolUse("u1", "Bash", rec{"command": "git status --secret"})),
		toolResult("u1", 2, strings.Repeat("x", 400), nil),
		call("m2", 3, nil), call("m3", 4, nil), boundary("b1", 5, "auto", 1000), call("m4", 6, nil))
	d := b.scan().data
	var row []float64
	for _, r := range d.rows("fills") {
		if d.name("labels", r[3]) == "Bash: git" && r[4] == 1 {
			row = r
		}
	}
	fatalf(t, row != nil, "no Bash: git result row")
	equal(t, row[5], 100, "400 characters at 4 per token")
	equal(t, total(d.rows("fills"), 6), 200, "two re-reads before compaction, none after")
	equal(t, total(d.rows("compactions"), 4), 1, "compactions")
}

func TestReadSplitsIntoWholeRangedAndImage(t *testing.T) {
	b := sandbox(t)
	png := make([]byte, 40000)
	binary.BigEndian.PutUint32(png[0:], 0x89504e47)
	binary.BigEndian.PutUint32(png[16:], 1500)
	binary.BigEndian.PutUint32(png[20:], 1000)
	image := []any{rec{"type": "image", "source": rec{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)}}}
	b.write(mainRel,
		call("m1", 1, nil, toolUse("a", "Read", rec{"file_path": "/a"}), toolUse("b", "Read", rec{"file_path": "/b", "offset": 1}),
			toolUse("c", "Read", rec{"file_path": "/c.png"})),
		toolResult("a", 2, strings.Repeat("a", 40), nil), toolResult("b", 2, strings.Repeat("b", 40), nil), toolResult("c", 2, image, nil))
	d := b.scan().data
	sizes := map[string]float64{}
	for _, r := range d.rows("fills") {
		sizes[d.name("labels", r[3])] = r[5]
	}
	want := map[string]float64{"Read: whole": 10, "Read: ranged": 10, "Read: image": 2000}
	fatalf(t, reflect.DeepEqual(sizes, want), "sizes %v", sizes)
}

func TestOutOfOrderDaysKeepTheirValuesThroughTheRemap(t *testing.T) {
	b := sandbox(t)
	day := func(d int) int { return dayMinutes * d }
	label := func(d int) string {
		ts, _ := time.Parse(time.RFC3339, T(day(d)))
		return aggregate.LocalDay(ts.Unix())
	}
	d2, d0, d1 := label(2), label(0), label(1)
	b.write(mainRel,
		call("later", day(2), rec{"input_tokens": 300}, toolUse("u2", "Bash", rec{"command": "git x"})),
		toolResult("u2", day(2), strings.Repeat("x", 40), rec{"is_error": true}),
		boundary("b2", day(2)+1, "auto", 22),
		call("first", day(0), rec{"input_tokens": 100}, toolUse("u0", "Bash", rec{"command": "npm x"})),
		toolResult("u0", day(0), strings.Repeat("x", 80), nil),
		call("middle", day(1), rec{"input_tokens": 200}))
	b.write(subRel, call("agent2", day(2), rec{"input_tokens": 3000}), call("agent0", day(0), rec{"input_tokens": 1000}))
	d := b.scan().data
	sorted := []string{d2, d0, d1}
	sort.Strings(sorted)
	fatalf(t, reflect.DeepEqual(d.strings("days"), sorted), "days %v", d.strings("days"))
	byDay := func(table string, col int) [3]float64 {
		var out [3]float64
		for i, l := range []string{d2, d0, d1} {
			out[i] = total(where(d.rows(table), func(r []float64) bool { return d.name("days", r[0]) == l }), col)
		}
		return out
	}
	checks := []struct {
		table string
		col   int
		want  [3]float64
	}{
		{"usage", 5, [3]float64{3300, 1100, 200}}, {"bandRows", 4, [3]float64{2, 2, 1}}, {"sessionRows", 4, [3]float64{2, 2, 1}},
		{"agentRows", 3, [3]float64{1, 1, 0}}, {"fills", 5, [3]float64{10, 20, 0}}, {"compactions", 5, [3]float64{22, 0, 0}},
		{"errors", 5, [3]float64{1, 0, 0}},
	}
	for _, c := range checks {
		equal(t, byDay(c.table, c.col), c.want, c.table)
	}
	equal(t, d.name("days", d.rows("agentRuns")[0][0]), d2, "a run counts on the day of its first call")
}

func TestAnIncrementalScanEqualsAFullScan(t *testing.T) {
	type step = []struct {
		rel     string
		records []any
	}
	steps := []step{
		{
			{mainRel, []any{call("m1", 1, nil, toolUse("u1", "Bash", rec{"command": "docker ps"}))}},
			{subRel, []any{call("s1", 2, rec{"cache_read_input_tokens": 500}, toolUse("s-u1", "Read", rec{"file_path": "/a"}))}},
		},
		{
			{mainRel, []any{toolResult("u1", 2, strings.Repeat("y", 80), nil), call("m1", 1, rec{"output_tokens": 40}),
				call("m2", 3, rec{"cache_read_input_tokens": 150000})}},
			{subRel, []any{toolResult("s-u1", 3, strings.Repeat("z", 40), rec{"is_error": true}), call("s2", 4, rec{"cache_read_input_tokens": 900})}},
			{otherRel, []any{call("m1", 1, rec{"output_tokens": 60}), call("o1", 5, nil), boundary("b0", 6, "manual", 7)}},
		},
		{
			{mainRel, []any{boundary("b1", 5, "auto", 1000), call("m3", 6, nil, toolUse("u3", "Grep", rec{})),
				toolResult("u3", 7, "Exit code 1\nx", rec{"is_error": true})}},
			{subRel, []any{call("s3", 8, rec{"cache_read_input_tokens": 2000})}},
		},
	}
	incremental := sandbox(t)
	files := map[string]string{}
	all := map[string][]any{}
	var order []string
	var d data
	for _, s := range steps {
		for _, part := range s {
			if file, ok := files[part.rel]; ok {
				appendTo(t, file, part.records...)
			} else {
				files[part.rel] = incremental.write(part.rel, part.records...)
				order = append(order, part.rel)
			}
			all[part.rel] = append(all[part.rel], part.records...)
		}
		incremental.writeMeta(subRel, rec{"agentType": "qa-tester"})
		d = incremental.scan().data
	}
	full := sandbox(t)
	for _, rel := range order {
		full.write(rel, all[rel]...)
	}
	full.writeMeta(subRel, rec{"agentType": "qa-tester"})
	sameTables(t, d, full.scan().data, "incremental and full")
	for _, key := range []string{"usage", "bandRows", "compactions", "sessionRows", "agentRows", "agentRuns", "fills", "errors"} {
		fatalf(t, len(d.rows(key)) > 0, "%s is empty", key)
	}
	fatalf(t, len(d.strings("labels")) > 0, "labels is empty")
	equal(t, d.stat("uniqueCalls"), 7, "unique calls")
}

func TestLinesAcrossTheReadBoundaryAreReadWhole(t *testing.T) {
	b := sandbox(t)
	pad := func(n int) string { return strings.Repeat("x", n) }
	before := encodeLine(toolResult("nobody", 1, pad(1024*1024-500), nil))
	fatalf(t, len(before) < 1024*1024, "the first line fits one read")
	crossing := encodeLine(call("crossing", 2, rec{"output_tokens": 7}, rec{"type": "text", "text": pad(2000)}))
	fatalf(t, len(before)+1+len(crossing) > 1024*1024, "this line sits across the boundary")
	long := encodeLine(call("long", 3, rec{"output_tokens": 9}, rec{"type": "text", "text": pad(2_500_000)}))
	file := b.write(mainRel, string(before), string(crossing), string(long), call("after", 4, nil))
	d := b.scan().data
	equal(t, total(d.rows("usage"), 4), 3, "calls")
	equal(t, total(d.rows("usage"), 8), 17, "output")
	equal(t, d.stat("skipped"), 0, "skipped")
	equal(t, d.stat("lines"), 4, "lines")
	appendTo(t, file, call("resumed", 5, nil))
	d = b.scan().data
	equal(t, total(d.rows("usage"), 4), 4, "calls after resume")
	equal(t, d.stat("lines"), 5, "lines after resume")
	equal(t, d.stat("skipped"), 0, "skipped after resume")
}

func TestMultiByteAndLineSeparatorStayWholeAcrossAResume(t *testing.T) {
	b := sandbox(t)
	tricky := func(id string, min int) rec {
		text := "日本語 \U0001F600 a" + lineSeparator + "b" + lineSeparator + "c"
		return call(id, min, rec{"output_tokens": 1}, rec{"type": "text", "text": text})
	}
	first := string(encodeLine(tricky("m1", 1)))
	fatalf(t, strings.Contains(first, lineSeparator), "written raw, the way a transcript holds it")
	second := append(encodeLine(tricky("m2", 2)), '\n')
	cut := bytes.Index(second, []byte("\U0001F600")) + 2
	file := b.write(mainRel, first)
	appendBytes(t, file, second[:cut])
	d := b.scan().data
	equal(t, total(d.rows("usage"), 4), 1, "calls before the rest arrives")
	equal(t, d.stat("skipped"), 0, "skipped")
	appendBytes(t, file, second[cut:])
	appendTo(t, file, tricky("m3", 3))
	d = b.scan().data
	equal(t, total(d.rows("usage"), 4), 3, "calls")
	equal(t, d.stat("lines"), 3, "lines")
	equal(t, d.stat("skipped"), 0, "skipped")
	full := sandbox(t)
	full.write(mainRel, first, string(encodeLine(tricky("m2", 2))), tricky("m3", 3))
	sameTables(t, d, full.scan().data, "resumed and full")
}

func TestAHalfWrittenLineWaitsAndACorruptLineIsSkipped(t *testing.T) {
	b := sandbox(t)
	line := string(encodeLine(call("m2", 2, nil)))
	m1 := string(encodeLine(call("m1", 1, nil)))
	file := filepath.Join(b.root, mainRel)
	b.write(mainRel)
	must(t, os.WriteFile(file, []byte(m1+"\n{broken json\n"+line[:20]), 0o644))
	d := b.scan().data
	equal(t, total(d.rows("usage"), 4), 1, "calls")
	equal(t, d.stat("skipped"), 1, "skipped")
	must(t, os.WriteFile(file, []byte(m1+"\n{broken json\n"+line+"\n"), 0o644))
	d = b.scan().data
	equal(t, total(d.rows("usage"), 4), 2, "calls")
	equal(t, d.stat("skipped"), 1, "the corrupt line is not counted twice")
}

func TestARewrittenOrTruncatedTranscriptIsRescannedAndADeletedOneDisappears(t *testing.T) {
	b := sandbox(t)
	file := b.write(mainRel, call("m1", 1, nil), call("m2", 2, nil), call("m3", 3, nil))
	equal(t, total(b.scan().data.rows("usage"), 4), 3, "calls")
	b.write(mainRel, call("z1", 1, nil))
	equal(t, total(b.scan().data.rows("usage"), 4), 1, "rewritten shorter")
	b.write(mainRel, call("q1", 1, nil), call("q2", 2, nil), call("q3", 3, nil), call("q4", 4, nil), call("q5", 5, nil))
	equal(t, total(b.scan().data.rows("usage"), 4), 5, "same-or-larger rewrite")
	must(t, os.Truncate(file, 0))
	equal(t, total(b.scan().data.rows("usage"), 4), 0, "truncated")
	must(t, os.Remove(file))
	equal(t, b.scan().data.stat("transcripts"), 0, "transcripts")
	left, _ := os.ReadDir(filepath.Join(b.cache, "shards"))
	equal(t, len(left), 0, "shards left")
}

func TestToolErrorsAreClassifiedAndAgentTypesComeFromMeta(t *testing.T) {
	b := sandbox(t)
	b.write(subRel,
		call("s1", 1, rec{"cache_read_input_tokens": 1000}, toolUse("u1", "Bash", rec{"command": "git push"})),
		toolResult("u1", 2, "Exit code 1\nfatal", rec{"is_error": true}),
		call("s2", 3, rec{"cache_read_input_tokens": 5000}))
	b.writeMeta(subRel, rec{"agentType": "qa-tester", "description": "CANARY_DESCRIPTION"})
	d := b.scan().data
	fatalf(t, reflect.DeepEqual(d.strings("agentTypes"), []string{"qa-tester"}), "agent types %v", d.strings("agentTypes"))
	equal(t, d.name("errorClasses", d.rows("errors")[0][4]), "non-zero exit", "error class")
	run := d.rows("agentRuns")[0]
	equal(t, run[3], 1, "runs")
	equal(t, run[4], 1001, "first-call context")
	equal(t, run[5], 5001, "peak context")
}

func TestDenialKindsCountAsDeniedOrHook(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel,
		call("m1", 1, nil, toolUse("u1", "Bash", rec{"command": "a"}), toolUse("u2", "Bash", rec{"command": "b"}), toolUse("u3", "Bash", rec{"command": "c"})),
		with(toolResult("u1", 2, "rejected", rec{"is_error": true}), rec{"toolDenialKind": "user-rejected"}),
		with(toolResult("u2", 2, "PreToolUse:Bash hook error: x", rec{"is_error": true}), rec{"toolDenialKind": "permission-rule"}),
		toolResult("u3", 2, "Exit code 1\nbash: x: Permission denied", rec{"is_error": true}))
	d := b.scan().data
	classes := map[string]float64{}
	for _, r := range d.rows("errors") {
		classes[d.name("errorClasses", r[4])] = r[5]
	}
	want := map[string]float64{"denied": 1, "blocked by hook": 1, "non-zero exit": 1}
	fatalf(t, reflect.DeepEqual(classes, want), "classes %v", classes)
}

func TestHostileRecordsAreContained(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel,
		call("constructor", 1, nil, toolUse("constructor", "Bash", rec{"command": "git a"}), toolUse("__proto__", "Bash", rec{"command": "npm b"}),
			toolUse("toString", "Grep", rec{})),
		toolResult("constructor", 2, strings.Repeat("x", 40), nil),
		toolResult("__proto__", 2, strings.Repeat("x", 80), nil),
		toolResult("toString", 2, strings.Repeat("x", 120), nil),
		toolResult("hasOwnProperty", 2, "x", nil),
		call("__proto__", 3, nil),
		call("huge", 4, rec{"input_tokens": 1e308, "cache_creation_input_tokens": 1e308, "cache_read_input_tokens": 1e300, "output_tokens": 1e12}),
		call("cmd", 5, nil, toolUse("big", "Bash", rec{"command": "git " + strings.Repeat("x", 3_000_000)})),
		toolResult("big", 6, "x", nil),
		strings.Repeat("[", 200000)+strings.Repeat("]", 200000),
		"null", "5", `"text"`, "[]",
		rec{"type": "assistant", "message": "not an object"},
		rec{"type": "assistant", "message": rec{"id": "odd", "usage": 5, "content": "text"}},
		rec{"type": "assistant", "message": rec{"id": 9, "usage": rec{}}},
		rec{"type": "user", "message": rec{"content": []any{nil, 5, "x", rec{"type": "tool_result"}, rec{"type": "tool_result", "tool_use_id": 7}}}},
		rec{"type": "system", "subtype": "compact_boundary", "compactMetadata": "x"},
		call("last", 7, nil))
	d := b.scan().data
	text, _ := os.ReadFile(b.out)
	fatalf(t, !bytes.Contains(text, []byte("null,")) && !bytes.Contains(text, []byte("Inf")), "a number overflowed")
	equal(t, total(d.rows("usage"), 4), 6, "calls")
	equal(t, total(d.rows("usage"), 5), 4, "absurd usage counts as zero")
	equal(t, total(d.rows("usage"), 8), 4, "the absurd output too; the odd record has no output")
	results := map[string]float64{}
	for _, r := range d.rows("fills") {
		if r[4] > 0 {
			results[d.name("labels", r[3])] = r[5]
		}
	}
	equal(t, results["Bash: git"], 11, "Bash: git")
	equal(t, results["Bash: npm"], 20, "Bash: npm")
	equal(t, results["Grep"], 30, "Grep")
}

func TestContextBandsSitOnTheAgreedEdges(t *testing.T) {
	var got []int
	for _, c := range []int64{99999, 100000, 199999, 200000, 399999, 400000, 699999, 700000} {
		got = append(got, aggregate.BandOf(c))
	}
	fatalf(t, reflect.DeepEqual(got, []int{0, 1, 1, 2, 2, 3, 3, 4}), "bands %v", got)
}

func TestTheDumpCarriesNoGuardSection(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	_, ok := b.scan().data["guard"]
	fatalf(t, !ok, "guard")
}

func TestTimestampsFallOnTheLocalCalendarDay(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	ts, _ := time.Parse(time.RFC3339, T(1))
	fatalf(t, reflect.DeepEqual(b.scan().data.strings("days"), []string{ts.Local().Format("2006-01-02")}), "days")
}

func mtimes(t *testing.T, files ...string) []time.Time {
	var out []time.Time
	for _, f := range files {
		st, err := os.Stat(f)
		must(t, err)
		out = append(out, st.ModTime())
	}
	return out
}

func TestANoChangeScanLeavesDataAndCacheAloneAndOnlyMovesStatus(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	first := b.scan()
	bytesBefore, _ := os.ReadFile(b.out)
	files := []string{b.out, b.indexFile(), b.shardFile(mainRel)}
	before := mtimes(t, files...)
	time.Sleep(15 * time.Millisecond)
	second := b.scan()
	fatalf(t, strings.HasPrefix(second.stdout, "no transcript changed"), "stdout %q", second.stdout)
	after, _ := os.ReadFile(b.out)
	fatalf(t, bytes.Equal(after, bytesBefore), "data.json changed")
	fatalf(t, reflect.DeepEqual(mtimes(t, files...), before), "a file was rewritten")
	equal(t, second.data["generatedAt"], first.data["generatedAt"], "generatedAt means data last changed")
	fatalf(t, second.status["lastRunAt"].(string) > first.status["lastRunAt"].(string), "lastRunAt did not move")
	equal(t, second.status["ok"], any(true), "ok")
}

func TestARunThatFindsTheLockHeldLeavesDataAloneAndAFreshStatus(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	first := b.scan()
	bytesBefore, _ := os.ReadFile(b.out)
	must(t, os.WriteFile(filepath.Join(b.cache, "lock"), fmt.Appendf(nil, "%d\n%d\n", os.Getpid(), time.Now().UnixMilli()), 0o600))
	time.Sleep(15 * time.Millisecond)
	r := b.runWith(nil)
	must(t, r.err)
	fatalf(t, strings.Contains(r.stdout, "another scan is running"), "stdout %q", r.stdout)
	after, _ := os.ReadFile(b.out)
	fatalf(t, bytes.Equal(after, bytesBefore), "data.json changed")
	status := readJSON(t, filepath.Join(b.cache, "status.json"))
	fatalf(t, status["ok"] == true && status["code"] == "LOCKED", "status %v", status)
	fatalf(t, status["lastRunAt"].(string) > first.status["lastRunAt"].(string), "the page sees a live job, not an old time")
}

func statusKeys(status map[string]any) []string {
	var keys []string
	for k := range status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestStatusReportsSuccessAndFailureWithACodeOnly(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	ok := b.scan()
	fatalf(t, reflect.DeepEqual(statusKeys(ok.status), []string{"code", "lastRunAt", "ok"}), "keys %v", ok.status)
	fatalf(t, ok.status["ok"] == true && ok.status["code"] == nil, "status %v", ok.status)
	_, err := time.Parse(time.RFC3339, ok.status["lastRunAt"].(string))
	must(t, err)

	appendTo(t, filepath.Join(b.root, mainRel), call("m2", 2, nil))
	good, _ := os.ReadFile(b.out)
	blocked := filepath.Join(b.dir, "blocked", "data.json")
	must(t, os.MkdirAll(blocked, 0o755))
	r := b.runWith(func(o *options) { o.out = blocked })
	fatalf(t, r.err != nil, "a directory in place of the dump must fail the run")
	status := readJSON(t, filepath.Join(b.cache, "status.json"))
	fatalf(t, status["ok"] == false, "status %v", status)
	fatalf(t, codeShape.MatchString(status["code"].(string)) && strings.HasPrefix(status["code"].(string), "E"), "code %v", status["code"])
	after, _ := os.ReadFile(b.out)
	fatalf(t, bytes.Equal(after, good), "the old dump survives a failure")
	again := b.scan()
	fatalf(t, again.status["ok"] == true && again.status["code"] == nil, "recovered %v", again.status)
	equal(t, again.data.stat("uniqueCalls"), 2, "the failed run's change lands on the next run")
}

func TestALiveLockSkipsTheRunAndOtherLocksDoNot(t *testing.T) {
	b := sandbox(t)
	lock := filepath.Join(b.cache, "lock")
	b.write(mainRel, call("m1", 1, nil))
	must(t, os.MkdirAll(b.cache, 0o700))
	now := time.Now().UnixMilli()
	must(t, os.WriteFile(lock, fmt.Appendf(nil, "%d\n%d\n", os.Getpid(), now), 0o600))
	r := b.runWith(nil)
	must(t, r.err)
	fatalf(t, strings.Contains(r.stdout, "another scan is running"), "stdout %q", r.stdout)
	_, err := os.Stat(b.out)
	fatalf(t, os.IsNotExist(err), "no dump while locked")
	equal(t, readJSON(t, filepath.Join(b.cache, "status.json"))["code"], any("LOCKED"), "code")
	_, err = os.Stat(lock)
	must(t, err)
	minutes := func(n int64) int64 { return n * 60 * 1000 }
	cases := []struct{ name, content string }{
		{"stale", fmt.Sprintf("%d\n%d\n", os.Getpid(), now-minutes(11))},
		{"future", fmt.Sprintf("%d\n%d\n", os.Getpid(), now+minutes(60))},
		{"dead", fmt.Sprintf("2147483646\n%d\n", now)},
		{"garbage", "not a lock"},
		{"empty", ""},
		{"pidZero", fmt.Sprintf("0\n%d\n", now)},
	}
	for _, c := range cases {
		must(t, os.WriteFile(lock, []byte(c.content), 0o600))
		_ = os.Remove(b.out)
		s := b.scan()
		equal(t, s.status["ok"], any(true), c.name)
		equal(t, s.data.stat("transcripts"), 1, c.name)
		_, err := os.Stat(lock)
		fatalf(t, os.IsNotExist(err), "%s: lock not released", c.name)
	}
}

func TestIndexAndShardDisagreeingAfterACrashConverge(t *testing.T) {
	b := sandbox(t)
	file := b.write(mainRel, call("m1", 1, nil), call("m2", 2, nil))
	b.scan()
	oldIndex, _ := os.ReadFile(b.indexFile())
	oldShard, _ := os.ReadFile(b.shardFile(mainRel))
	appendTo(t, file, call("m3", 3, rec{"output_tokens": 5}), call("m1", 1, rec{"output_tokens": 9}))
	expected := b.scan().data
	key := bwsource.Key(clients.ClaudeCode, mainRel)
	repeat := func(name string, damage func()) {
		damage()
		d := b.scan().data
		sameTables(t, d, expected, name)
		equal(t, total(d.rows("usage"), 4), 3, name)
	}
	repeat("crash after the shard write, before the index write", func() { must(t, os.WriteFile(b.indexFile(), oldIndex, 0o600)) })
	repeat("shard older than the index", func() { must(t, os.WriteFile(b.shardFile(mainRel), oldShard, 0o600)) })
	repeat("shard missing", func() { must(t, os.Remove(b.shardFile(mainRel))) })
	repeat("index missing", func() { must(t, os.Remove(b.indexFile())) })
	repeat("index corrupt", func() { must(t, os.WriteFile(b.indexFile(), []byte(`{"v":9,`), 0o600)) })
	repeat("index entry malformed", func() {
		must(t, os.WriteFile(b.indexFile(), []byte(`{"v":9,"files":{"`+key+`":{"size":"x"}}}`), 0o600))
	})
	orphan := filepath.Join(b.cache, "shards", strings.Repeat("0", 32)+".json")
	must(t, os.WriteFile(orphan, []byte("{}"), 0o600))
	must(t, os.WriteFile(orphan+".0123456789abcdef.tmp", []byte("{}"), 0o600))
	b.scan()
	left, _ := os.ReadDir(filepath.Join(b.cache, "shards"))
	fatalf(t, len(left) == 1 && left[0].Name() == key+".json", "orphans are swept: %d left", len(left))
}

func shardEvents(t *testing.T, file string) []any {
	t.Helper()
	return readJSON(t, file)["events"].([]any)
}

func TestACorruptShardIsRebuiltInTheSameRun(t *testing.T) {
	b := sandbox(t)
	mainFile := b.write(mainRel, call("m1", 1, rec{"input_tokens": 10}), call("m2", 2, rec{"input_tokens": 20}))
	b.write(otherRel, call("o1", 3, rec{"input_tokens": 40}))
	expected := b.scan().data
	shard := b.shardFile(mainRel)
	good, _ := os.ReadFile(shard)
	for _, c := range []struct{ name, text string }{{"garbage", "not json at all"}, {"truncated", string(good[:40])}} {
		must(t, os.WriteFile(shard, []byte(c.text), 0o600))
		d := b.scan().data
		sameTables(t, d, expected, c.name)
		equal(t, d.stat("unrebuilt"), 0, c.name)
		equal(t, len(shardEvents(t, shard)), 2, c.name+": rebuilt on disk")
	}
	sameSize := "{" + strings.Repeat(" ", len(good)-2) + "}"
	must(t, os.WriteFile(shard, []byte(sameSize), 0o600))
	fatalf(t, strings.HasPrefix(b.scan().stdout, "no transcript changed"), "a no-change scan does not parse shards")
	appendTo(t, filepath.Join(b.root, otherRel), call("o2", 4, rec{"input_tokens": 80}))
	s := b.scan()
	fatalf(t, strings.Contains(s.stdout, "1 rebuilt"), "stdout %q", s.stdout)
	equal(t, total(s.data.rows("usage"), 4), 4, "found and rebuilt as soon as an output is written")
	equal(t, s.data["scan"].(map[string]any)["rebuilt"], any(1.0), "rebuilt")
	equal(t, s.data.stat("unrebuilt"), 0, "unrebuilt")

	must(t, os.WriteFile(shard, []byte(sameSize), 0o600))
	must(t, os.Remove(mainFile))
	gone := b.scan().data
	equal(t, gone.stat("transcripts"), 1, "a deleted transcript leaves with its shard")
	equal(t, gone.stat("unrebuilt"), 0, "unrebuilt")
}

func TestTheShardValidatorRejectsEachKindOfBadShard(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, rec{"input_tokens": 10}), call("m2", 2, rec{"input_tokens": 20}))
	other := b.write(otherRel, call("o1", 3, rec{"input_tokens": 40}))
	b.scan()
	shard := b.shardFile(mainRel)
	key := bwsource.Key(clients.ClaudeCode, mainRel)
	parsed := readJSON(t, shard)
	events := parsed["events"].([]any)
	mapEvents := func(i int, v any) []any {
		var out []any
		for _, e := range events {
			copied := append([]any{}, e.([]any)...)
			copied[i] = v
			out = append(out, copied)
		}
		return out
	}
	variant := func(change rec) map[string]any {
		out := map[string]any{}
		for k, v := range parsed {
			out[k] = v
		}
		for k, v := range change {
			out[k] = v
		}
		return out
	}
	damaged := []struct {
		name  string
		value map[string]any
	}{
		{"wrongVersion", variant(rec{"v": 3})},
		{"wrongKey", variant(rec{"key": strings.Repeat("f", 32)})},
		{"eventsNotArray", variant(rec{"events": 5})},
		{"badEvent", variant(rec{"events": []any{[]any{0, "x"}}})},
		{"badNumber", variant(rec{"events": mapEvents(4, -1)})},
		{"badTimestamp", variant(rec{"events": mapEvents(1, "yesterday")})},
		{"badId", variant(rec{"events": mapEvents(2, 7)})},
		{"badLabel", variant(rec{"events": mapEvents(3, 7)})},
		{"badPending", variant(rec{"pending": rec{"a": 1}})},
		{"emptied", variant(rec{"events": []any{}, "offset": 0})},
	}
	calls := 3.0
	for _, c := range damaged {
		text := encodeLine(c.value)
		must(t, os.WriteFile(shard, text, 0o600))
		index := readJSON(t, b.indexFile())
		entry := index["files"].(map[string]any)[key].(map[string]any)
		entry["shardBytes"], entry["shardHash"] = len(text), extract.HashKey(text)
		must(t, os.WriteFile(b.indexFile(), encodeLine(index), 0o600))
		calls++
		appendTo(t, other, call("force-"+c.name, 4, nil))
		d := b.scan().data
		equal(t, d["scan"].(map[string]any)["rebuilt"], any(1.0), c.name+": found by the validator when the shard was loaded")
		equal(t, d.stat("unrebuilt"), 0, c.name)
		equal(t, total(d.rows("usage"), 4), calls, c.name)
		fatalf(t, reflect.DeepEqual(shardEvents(t, shard), events), "%s: not rebuilt on disk", c.name)
	}
}

func TestSameSizeValidDamageIsFoundByTheChecksum(t *testing.T) {
	b := sandbox(t)
	mainFile := b.write(mainRel, call("m1", 1, rec{"output_tokens": 111}))
	other := b.write(otherRel, call("o1", 2, rec{"output_tokens": 1}))
	b.scan()
	shard := b.shardFile(mainRel)
	good, _ := os.ReadFile(shard)
	flipped := bytes.Replace(good, []byte(",111,null]"), []byte(",999,null]"), 1)
	fatalf(t, !bytes.Equal(flipped, good), "the flip changed nothing")
	must(t, os.WriteFile(shard, flipped, 0o600))
	fatalf(t, strings.HasPrefix(b.scan().stdout, "no transcript changed"), "a no-change scan reads no shard")
	appendTo(t, other, call("o2", 3, rec{"output_tokens": 1}))
	d := b.scan().data
	equal(t, d["scan"].(map[string]any)["rebuilt"], any(1.0), "rebuilt")
	equal(t, total(d.rows("usage"), 8), 113, "the true output tokens, not the flipped ones")
	now, _ := os.ReadFile(shard)
	fatalf(t, bytes.Equal(now, good), "rebuilt shard differs")

	must(t, os.WriteFile(shard, flipped, 0o600))
	appendTo(t, mainFile, call("m2", 4, rec{"output_tokens": 5}))
	equal(t, total(b.scan().data.rows("usage"), 8), 118, "growth does not resume from the damaged shard and bless it")
}

func TestTheOutputIsComparedByHashNeverParsed(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.scan()
	good, _ := os.ReadFile(b.out)
	fake := bytes.Replace(good, []byte(`"uniqueCalls":1`), []byte(`"uniqueCalls":9`), 1)
	fatalf(t, !bytes.Equal(fake, good), "no fake")
	must(t, os.WriteFile(b.out, fake, 0o600))
	equal(t, b.scan().data.stat("uniqueCalls"), 1, "same size, other bytes: rewritten")
	must(t, os.WriteFile(b.out, []byte(`{"version":3,"stats":{"unreadable":0,"unrebuilt":0}}`), 0o600))
	equal(t, b.scan().data.stat("uniqueCalls"), 1, "a short file with plausible stats is not trusted")
	must(t, os.WriteFile(b.out, []byte("x"), 0o600))
	must(t, os.Truncate(b.out, 20<<30))
	started := time.Now()
	must(t, b.runWith(nil).err)
	fatalf(t, time.Since(started) < 10*time.Second, "the 20 GB file is refused by its size, not read")
	equal(t, data(readJSON(t, b.out)).stat("uniqueCalls"), 1, "rewritten")
}

func TestAFailedOutputWriteIsRetriedByTheNextScan(t *testing.T) {
	b := sandbox(t)
	mainFile := b.write(mainRel, call("m1", 1, nil))
	b.scan()
	appendTo(t, mainFile, call("m2", 2, nil))
	before, _ := os.ReadFile(b.out)
	must(t, os.Remove(b.out))
	must(t, os.Mkdir(b.out, 0o755))
	fatalf(t, b.runWith(nil).err != nil, "a directory in place of the dump must fail")
	must(t, os.RemoveAll(b.out))
	must(t, os.WriteFile(b.out, before, 0o600))
	equal(t, b.scan().data.stat("uniqueCalls"), 2, "retried")
}
