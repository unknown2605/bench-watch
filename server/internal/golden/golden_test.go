package golden

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/scan"
)

// The golden file was written by the scanner before CM-00016 put it behind source.Reader; rewrite it only on purpose.
var update = flag.Bool("update", false, "rewrite testdata/replay.json")

const goldenFile = "testdata/replay.json"

func TestMain(m *testing.M) {
	time.Local = time.FixedZone("ICT", 7*3600)
	// Project labels strip the home folder's prefix; the fixture's folders are named for this home.
	if err := os.Setenv("HOME", "/Users/x"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	fixed := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
		return os.Chtimes(target, fixed, fixed)
	})
	if err != nil {
		t.Fatal(err)
	}
}

type replayed struct {
	Rows map[string]any  `json:"rows"`
	Data *aggregate.Data `json:"data"`
}

func replay(t *testing.T, root, cache string) []byte {
	t.Helper()
	st, err := scan.UpdateShards(root, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := aggregate.BuildRows(st.Shards, aggregate.MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := aggregate.Aggregate(st.Shards, aggregate.MaxRunItems)
	if err != nil {
		t.Fatal(err)
	}
	out := replayed{Data: data, Rows: map[string]any{
		"usage": rows.Usage, "contextBands": rows.ContextBands, "compactions": rows.Compactions,
		"boundaries": rows.Boundaries, "sessions": rows.Sessions, "subagents": rows.Subagents,
		"subagentRuns": rows.SubagentRuns, "results": rows.Results, "resultSizes": rows.ResultSizes,
		"topResults": rows.TopResults, "errors": rows.Errors, "itemsDropped": rows.ItemsDropped,
		"uniqueCalls": rows.UniqueCalls, "transcripts": rows.Transcripts, "duplicateLines": rows.DuplicateLines,
		"lines": rows.Lines, "skipped": rows.Skipped, "unpriced": rows.Unpriced, "days": rows.Days(),
		"contributorDays": len(rows.Contributors), "shards": len(rows.Keys),
	}}
	text, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(text, '\n')
}

// withoutClient checks every usage, context-band and session row carries client claude-code and no other table has
// one, then drops it, so the rest of each row compares byte for byte with the file written before the column existed.
func withoutClient(t *testing.T, text []byte) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(text, &doc); err != nil {
		t.Fatal(err)
	}
	clientTables := map[string]bool{"usage": true, "contextBands": true, "sessions": true}
	for name, table := range doc["rows"].(map[string]any) {
		list, ok := table.([]any)
		if !ok {
			continue
		}
		for _, row := range list {
			fields, ok := row.(map[string]any)
			if !ok {
				continue
			}
			client, has := fields["Client"]
			if has != clientTables[name] {
				t.Fatalf("%s: client column present=%v, want %v", name, has, clientTables[name])
			}
			if has && client != "claude-code" {
				t.Fatalf("%s: client %v, want claude-code", name, client)
			}
			delete(fields, "Client")
		}
	}
	data := doc["data"].(map[string]any)
	unpriced, _ := data["unpriced"].([]any)
	owners, _ := data["unpricedClients"].([]any)
	if len(owners) != len(unpriced) {
		t.Fatalf("unpricedClients %v do not pair with unpriced %v", owners, unpriced)
	}
	for _, c := range owners {
		if c != "claude-code" {
			t.Fatalf("unpriced client %v, want claude-code", c)
		}
	}
	delete(data, "unpricedClients")
	out, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func canonical(t *testing.T, text []byte) []byte {
	t.Helper()
	var doc any
	if err := json.Unmarshal(text, &doc); err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestReplayMatchesTheGoldenFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "projects")
	copyTree(t, "testdata/projects", root)
	cache := t.TempDir()
	got := replay(t, root, cache)
	if *update {
		if err := os.WriteFile(goldenFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withoutClient(t, got), canonical(t, want)) {
		failed := filepath.Join(t.TempDir(), "replay.json")
		_ = os.WriteFile(failed, got, 0o600)
		t.Fatalf("replay differs from %s; got written to %s", goldenFile, failed)
	}
	if again := replay(t, root, cache); !bytes.Equal(again, got) {
		t.Fatal("a second scan over the warm cache gave other rows")
	}
}
