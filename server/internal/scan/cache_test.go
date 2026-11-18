package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func writeCall(t *testing.T, file, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"claude-opus-5-5",`+
		`"content":[],"usage":{"input_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,`+
		`"output_tokens":1}}}`+"\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), id)
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func keysOf(st *Store) []string {
	var out []string
	for _, f := range st.filesByKey {
		out = append(out, f.rel)
	}
	sort.Strings(out)
	return out
}

// A cache kept across runs must see every kind of change a fresh scan sees, with no shard read on an idle run.
func TestCacheSeesWhatAFreshScanSees(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	main := filepath.Join(root, "-p", "aaaaaaaa-0000-0000-0000-000000000000.jsonl")
	writeCall(t, main, "m1")
	cache := NewCache(root, filepath.Join(t.TempDir(), "cache"))
	sub := filepath.Join(root, "-p", "aaaaaaaa-0000-0000-0000-000000000000", "subagents", "agent-a1.jsonl")
	other := filepath.Join(root, "-q", "bbbbbbbb-0000-0000-0000-000000000000.jsonl")
	steps := []struct {
		name   string
		change func()
		want   int
	}{
		{"first", func() {}, 1},
		{"idle", func() {}, 0},
		{"append", func() { writeCall(t, main, "m2") }, 1},
		{"new folder below a known one", func() { writeCall(t, sub, "s1") }, 1},
		{"new project folder", func() { writeCall(t, other, "o1") }, 1},
		{"append in a deep folder", func() { writeCall(t, sub, "s2") }, 1},
		{"rename", func() {
			if err := os.Rename(other, filepath.Join(root, "-q", "cccccccc-0000-0000-0000-000000000000.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, 1},
		{"remove a folder", func() {
			if err := os.RemoveAll(filepath.Join(root, "-p", "aaaaaaaa-0000-0000-0000-000000000000")); err != nil {
				t.Fatal(err)
			}
		}, 0},
		{"idle again", func() {}, 0},
	}
	for _, step := range steps {
		step.change()
		st, err := cache.Update(nil)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		fresh, err := UpdateShards(root, t.TempDir(), nil)
		if err != nil {
			t.Fatalf("%s, fresh scan: %v", step.name, err)
		}
		if got, want := keysOf(st), keysOf(fresh); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: the cached run lists %v, a fresh scan %v", step.name, got, want)
		}
		for key, entry := range fresh.Entries {
			if st.Entries[key].ShardHash != entry.ShardHash {
				t.Fatalf("%s: shard of %s differs from a fresh scan", step.name, st.filesByKey[key].rel)
			}
		}
		if st.Changed != step.want || (step.want == 0 && st.ShardReads != 0) {
			t.Fatalf("%s: %d changed, %d shards read; want %d changed", step.name, st.Changed, st.ShardReads, step.want)
		}
	}
}
