package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"benchwatch/server/internal/clients"
	bwsource "benchwatch/server/internal/source"
)

const (
	mainRel    = "-Users-x-workspace-proj/aaaaaaaa-0000-0000-0000-000000000000.jsonl"
	subRel     = "-Users-x-workspace-proj/aaaaaaaa-0000-0000-0000-000000000000/subagents/agent-1.jsonl"
	otherRel   = "-Users-x-workspace-proj/bbbbbbbb-0000-0000-0000-000000000000.jsonl"
	dayMinutes = 24 * 60
	childEnv   = "SCAN_DUMP_TEST_CHILD"
)

var lineSeparator = string(rune(0x2028))

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Setenv("BENCHWATCH_WEB_DIR", filepath.Join(os.TempDir(), "scan-dump-test-web"))
	os.Exit(m.Run())
}

type rec = map[string]any

type box struct {
	t                     *testing.T
	dir, root, cache, out string
}

func sandbox(t *testing.T) *box {
	t.Helper()
	dir := t.TempDir()
	b := &box{t, dir, filepath.Join(dir, "projects"), filepath.Join(dir, "cache"), filepath.Join(dir, "out", "data.json")}
	must(t, os.MkdirAll(b.root, 0o755))
	return b
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (b *box) options() options {
	return options{out: b.out, cache: b.cache, projects: b.root}
}

// encodeLine keeps U+2028 raw, the way a transcript holds it; encoding/json always escapes it.
func encodeLine(record any) []byte {
	if s, ok := record.(string); ok {
		return []byte(s)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(record); err != nil {
		panic(err)
	}
	line := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return bytes.ReplaceAll(line, []byte(`\`+"u2028"), []byte(lineSeparator))
}

func asLines(records []any) []byte {
	var out []byte
	for _, r := range records {
		out = append(append(out, encodeLine(r)...), '\n')
	}
	return out
}

func (b *box) write(rel string, records ...any) string {
	file := filepath.Join(b.root, rel)
	must(b.t, os.MkdirAll(filepath.Dir(file), 0o755))
	must(b.t, os.WriteFile(file, asLines(records), 0o644))
	return file
}

func appendTo(t *testing.T, file string, records ...any) {
	t.Helper()
	appendBytes(t, file, asLines(records))
}

func appendBytes(t *testing.T, file string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = f.Write(data)
	must(t, err)
	must(t, f.Close())
}

func (b *box) writeMeta(rel string, meta rec) {
	must(b.t, os.WriteFile(filepath.Join(b.root, strings.TrimSuffix(rel, ".jsonl")+".meta.json"), encodeLine(meta), 0o644))
}

type result struct {
	stdout string
	err    error
}

func (b *box) runWith(change func(*options)) result {
	var out bytes.Buffer
	o := b.options()
	o.stdout = &out
	if change != nil {
		change(&o)
	}
	err := runAndRecord(o)
	return result{out.String(), err}
}

type data map[string]any

type scanned struct {
	stdout string
	data   data
	status map[string]any
}

func readJSON(t *testing.T, file string) map[string]any {
	t.Helper()
	text, err := os.ReadFile(file)
	must(t, err)
	var v map[string]any
	must(t, json.Unmarshal(text, &v))
	return v
}

func (b *box) scanWith(change func(*options)) scanned {
	b.t.Helper()
	r := b.runWith(change)
	if r.err != nil {
		b.t.Fatalf("scan failed: %s", codeOf(r.err))
	}
	return scanned{r.stdout, readJSON(b.t, b.out), readJSON(b.t, filepath.Join(b.cache, "status.json"))}
}

func (b *box) scan() scanned { b.t.Helper(); return b.scanWith(nil) }

func (b *box) shardFile(rel string) string {
	return filepath.Join(b.cache, "shards", bwsource.Key(clients.ClaudeCode, rel)+".json")
}

func (b *box) indexFile() string { return filepath.Join(b.cache, "index.json") }

func (d data) rows(name string) [][]float64 {
	var out [][]float64
	list, _ := d[name].([]any)
	for _, r := range list {
		var row []float64
		for _, v := range r.([]any) {
			row = append(row, v.(float64))
		}
		out = append(out, row)
	}
	return out
}

func (d data) strings(name string) []string {
	var out []string
	list, _ := d[name].([]any)
	for _, v := range list {
		out = append(out, v.(string))
	}
	return out
}

func (d data) name(dim string, i float64) string { return d.strings(dim)[int(i)] }

func (d data) session(i float64) string { return d["sessions"].([]any)[int(i)].([]any)[0].(string) }

func (d data) stat(name string) float64 {
	v, ok := d["stats"].(map[string]any)[name].(float64)
	if !ok {
		return -1
	}
	return v
}

func (d data) tables() map[string]any {
	out := map[string]any{}
	for k, v := range d {
		if k != "generatedAt" && k != "scan" && k != "guard" {
			out[k] = v
		}
	}
	return out
}

func sameTables(t *testing.T, a, b data, name string) {
	t.Helper()
	if !reflect.DeepEqual(a.tables(), b.tables()) {
		ja, _ := json.Marshal(a.tables())
		jb, _ := json.Marshal(b.tables())
		t.Fatalf("%s: tables differ\n%s\n%s", name, ja, jb)
	}
}

func total(rows [][]float64, i int) float64 {
	sum := 0.0
	for _, r := range rows {
		sum += r[i]
	}
	return sum
}

func where(rows [][]float64, keep func([]float64) bool) [][]float64 {
	var out [][]float64
	for _, r := range rows {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func T(min int) string {
	return time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC).Add(time.Duration(min) * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

func call(id string, min int, usage rec, content ...any) rec {
	u := rec{"input_tokens": 1, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0, "output_tokens": 1}
	for k, v := range usage {
		u[k] = v
	}
	if content == nil {
		content = []any{}
	}
	return rec{"type": "assistant", "timestamp": T(min), "message": rec{"id": id, "model": "claude-opus-5-5", "content": content, "usage": u}}
}

func withModel(r rec, model string) rec {
	r["message"].(rec)["model"] = model
	return r
}

func with(r rec, extra rec) rec {
	for k, v := range extra {
		r[k] = v
	}
	return r
}

func toolUse(id, name string, input rec) rec {
	return rec{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(id string, min int, text any, extra rec) rec {
	block := rec{"type": "tool_result", "tool_use_id": id, "content": text}
	for k, v := range extra {
		block[k] = v
	}
	return rec{"type": "user", "timestamp": T(min), "message": rec{"role": "user", "content": []any{block}}}
}

func boundary(uuid string, min int, trigger string, pre int) rec {
	return rec{"type": "system", "subtype": "compact_boundary", "uuid": uuid, "timestamp": T(min),
		"compactMetadata": rec{"trigger": trigger, "preTokens": pre}}
}

type child struct {
	stdout, stderr string
	exit           int
	maxRSS         int64
	cpu            time.Duration
}

// runChild runs main in a fresh process, so its peak RSS and CPU time are its own.
func (b *box) runChild(args ...string) child {
	b.t.Helper()
	argv := append([]string{"-projects", b.root}, args...)
	if len(args) == 0 {
		argv = append(argv, b.out, b.cache)
	}
	cmd := exec.Command(os.Args[0], argv...)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if _, ok := err.(*exec.ExitError); err != nil && !ok {
		b.t.Fatal(err)
	}
	usage := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	return child{stdout.String(), stderr.String(), cmd.ProcessState.ExitCode(), usage.Maxrss,
		cmd.ProcessState.UserTime() + cmd.ProcessState.SystemTime()}
}

func fatalf(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

func equal[V comparable](t *testing.T, got, want V, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}
