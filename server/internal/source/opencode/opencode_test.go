package opencode

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

const t0 = int64(1_790_000_000_000)

var v1Schema = []string{
	`CREATE TABLE session (id text PRIMARY KEY, project_id text, parent_id text, slug text, directory text,
		title text, version text, time_created integer, time_updated integer)`,
	`CREATE TABLE message (id text PRIMARY KEY, session_id text, time_created integer, time_updated integer, data text)`,
	`CREATE TABLE part (id text PRIMARY KEY, message_id text, session_id text, time_created integer,
		time_updated integer, data text)`,
	`CREATE TABLE session_message (id text PRIMARY KEY, session_id text, type text, seq integer, data text)`,
}

var v2Schema = []string{
	`CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text, parent_id text, directory text, cost real,
		tokens_input integer, tokens_output integer, tokens_reasoning integer, tokens_cache_read integer,
		tokens_cache_write integer, time_created integer, time_updated integer)`,
	`CREATE TABLE session_message (id text PRIMARY KEY, session_id text, type text, seq integer,
		time_created integer, time_updated integer, data text)`,
}

func reply(created int64, in, out, reasoning, read, write int64, cost string, extra string) string {
	if cost != "" {
		cost = `,"cost":` + cost
	}
	return fmt.Sprintf(`{"role":"assistant","providerID":"anthropic","modelID":"claude-opus-4-5","time":{"created":%d,`+
		`"completed":%d},"tokens":{"input":%d,"output":%d,"reasoning":%d,"cache":{"read":%d,"write":%d}}%s%s}`,
		created, created+500, in, out, reasoning, read, write, cost, extra)
}

type fixture struct {
	home string
	db   *sql.DB
}

func newDB(t *testing.T, schema []string) fixture {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{home: home, db: db}
}

func (f fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) session(t *testing.T, id, parent string, created int64) {
	t.Helper()
	var p any
	if parent != "" {
		p = parent
	}
	f.exec(t, `INSERT INTO session (id, parent_id, directory, time_created, time_updated) VALUES (?, ?, ?, ?, ?)`,
		id, p, filepath.Join(os.Getenv("HOME"), "work", "proj"), created, created)
}

func (f fixture) message(t *testing.T, id, session string, updated int64, data string) {
	t.Helper()
	f.exec(t, `INSERT OR REPLACE INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)`,
		id, session, updated, updated, data)
}

type reading struct {
	cursors map[string]source.Cursor
}

func readAll(t *testing.T, r *Reader, last reading) reading {
	t.Helper()
	d, err := r.Discover()
	if err != nil {
		t.Fatal(err)
	}
	out := reading{cursors: map[string]source.Cursor{}}
	for _, u := range d.Units {
		f, _, err := source.Open(r.Roots(), u.Path)
		if err != nil {
			t.Fatal(err)
		}
		cur, err := r.Read(f, u, last.cursors[u.Key], source.Options{MaxItems: 1000})
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		out.cursors[u.Key] = cur
	}
	return out
}

type total struct {
	n      [5]int64
	usd    float64
	calls  int
	priced int
}

func (rd reading) total() total {
	type best struct {
		n      [5]int64
		usd    float64
		hasUSD bool
	}
	merged := map[string]*best{}
	for _, c := range rd.cursors {
		for _, e := range c.Events {
			b := merged[e.ID]
			if b == nil {
				b = &best{}
				merged[e.ID] = b
			}
			for i := range b.n {
				b.n[i] = max(b.n[i], e.N[i])
			}
			if e.HasUSD {
				b.usd, b.hasUSD = max(b.usd, e.USD), true
			}
		}
	}
	var out total
	for _, b := range merged {
		for i := range out.n {
			out.n[i] += b.n[i]
		}
		out.calls++
		if b.hasUSD {
			out.usd += b.usd
			out.priced++
		}
	}
	return out
}

func TestV1CountsMessagesOnceAcrossForksPartsAndReReads(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_A", "", t0)
	f.session(t, "ses_B", "ses_A", t0+10)
	f.session(t, "ses_F", "", t0+20)
	f.message(t, "msg_u", "ses_A", t0, `{"role":"user","time":{"created":1}}`)
	f.message(t, "msg_1", "ses_A", t0+1000, reply(t0+1000, 100, 10, 5, 1000, 50, "0.5", ""))
	f.message(t, "msg_2", "ses_A", t0+2000, reply(t0+2000, 20, 2, 0, 500, 0, "0", `,"summary":true`))
	// step-finish parts repeat msg_1's tokens and are never read
	f.exec(t, `INSERT INTO part VALUES ('p1', 'msg_1', 'ses_A', 1, 1, ?)`,
		`{"type":"step-finish","tokens":{"input":100,"output":10,"reasoning":5,"cache":{"read":1000,"write":50}}}`)
	// ses_F is a fork: msg_1 cloned under a new id; ses_B is a sub-agent with its own call and no cost
	f.message(t, "msg_1f", "ses_F", t0+3000, reply(t0+1000, 100, 10, 5, 1000, 50, "0.5", ""))
	f.message(t, "msg_b", "ses_B", t0+1500, reply(t0+1500, 7, 1, 0, 0, 0, "", ""))
	f.message(t, "msg_open", "ses_A", t0+2500, `{"role":"assistant","modelID":"x","time":{"created":1},"tokens":{}}`)
	r := New(f.home)
	first := readAll(t, r, reading{})
	got := first.total()
	want := [5]int64{127, 50, 0, 1500, 18}
	if got.n != want || got.calls != 3 || got.priced != 2 || got.usd != 0.5 {
		t.Fatalf("%+v; want %v over 3 calls, 2 priced at $0.5 (msg_2's 0 is a price, msg_b has none)", got, want)
	}
	if len(first.cursors) != 3 {
		t.Fatalf("units %d, want one per session", len(first.cursors))
	}

	// The open message finishes later; a re-read from the watermark adds it and nothing else.
	f.message(t, "msg_open", "ses_A", t0+9000, reply(t0+2500, 1, 1, 0, 0, 0, "0.01", ""))
	again := readAll(t, r, first).total()
	if again.calls != 4 || again.n != [5]int64{128, 50, 0, 1500, 19} || again.usd != 0.51 {
		t.Errorf("after the update: %+v", again)
	}
	for _, c := range readAll(t, r, first).cursors {
		if c.Offset != headerBytes || c.LastTs < t0/1000 {
			t.Errorf("cursor %+v", c)
		}
	}
}

func TestV2UsesMessagesNotTheCumulativeSessionColumns(t *testing.T) {
	f := newDB(t, v2Schema)
	f.exec(t, `INSERT INTO session_v2 (id, directory, time_created, time_updated, cost, tokens_input, tokens_output)
		VALUES ('ses_v', '/x', ?, ?, 99, 1000000, 1000000)`, t0, t0)
	data := `{"providerID":"openai","modelID":"gpt-5","time":{"created":1790000001000,"completed":1790000002000},` +
		`"cost":0.2,"tokens":{"input":40,"output":4,"reasoning":1,"cache":{"read":400,"write":0}}}`
	f.exec(t, `INSERT INTO session_message VALUES ('m1', 'ses_v', 'assistant', 1, ?, ?, ?)`, t0, t0, data)
	f.exec(t, `INSERT INTO session_message VALUES ('m2', 'ses_v', 'user', 2, ?, ?, ?)`, t0, t0, data)
	got := readAll(t, New(f.home), reading{}).total()
	if got.calls != 1 || got.n != [5]int64{40, 0, 0, 400, 5} || got.usd != 0.2 {
		t.Errorf("%+v", got)
	}
	db, _ := New(f.home).open()
	defer db.Close()
	if g, err := detect(db); g != V2 || err != nil {
		t.Errorf("generation %v, %v", g, err)
	}
}

func TestTheStoreFilesSessionsAndCosts(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_A", "", t0)
	f.message(t, "msg_1", "ses_A", t0+1000, reply(t0+1000, 100, 10, 0, 0, 0, "0.25", ""))
	cache, err := scan.NewCacheFor([]source.Reader{New(f.home)}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := cache.Update(nil)
	if err != nil || st.Unreadable != 0 {
		t.Fatalf("%v, unreadable %d (%v)", err, st.Unreadable, st.FileErr)
	}
	var shards []*scan.Shard
	if err := st.Shards(func(s *scan.Shard) error { shards = append(shards, s); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(shards) != 1 || shards[0].Project != "work-proj" || len(shards[0].Session) != 8 ||
		len(shards[0].Events) != 1 || shards[0].Events[0].USD != 0.25 {
		t.Errorf("shards %+v", shards)
	}
}

func TestTheReaderOpensOnlyTheDatabase(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_A", "", t0)
	r := New(f.home)
	for _, name := range []string{"auth.json", "mcp-auth.json", ".env"} {
		full := filepath.Join(f.home, name)
		if err := os.WriteFile(full, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := source.Open(r.Roots(), full); !errors.Is(err, source.ErrForbiddenName) {
			t.Errorf("%s: %v, want ErrForbiddenName", name, err)
		}
	}
	if _, _, err := source.Open(r.Roots(), filepath.Join(filepath.Dir(f.home), "x.db")); !errors.Is(err,
		source.ErrOutsideRoots) {
		t.Errorf("outside: %v", err)
	}
	d, err := r.Discover()
	if err != nil || len(d.Units) != 1 || d.Units[0].Path != filepath.Join(f.home, dbFile) {
		t.Errorf("units %+v, %v", d.Units, err)
	}
	if New(t.TempDir()).Detect() {
		t.Error("detected without a database")
	}
}

func TestABadSessionRowIsCountedAndTheRestStillGetTheirIdentity(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_good", "", t0)
	f.session(t, "ses_bad", "", t0)
	f.exec(t, `UPDATE session SET time_created = 'yesterday' WHERE id = 'ses_bad'`)
	f.exec(t, `INSERT INTO session (id, directory, time_created) VALUES (NULL, '/x', ?)`, t0)
	r := New(f.home)
	d, err := r.Discover()
	if err != nil || len(d.Units) != 1 || len(d.UnreadableKeys) != 1 || d.UnreadableDirs != 1 {
		t.Fatalf("units %d unreadable keys %v dirs %d, %v", len(d.Units), d.UnreadableKeys, d.UnreadableDirs, err)
	}
	id, err := r.Identify(d.Units[0], nil)
	if err != nil || id.Project != "work-proj" || id.Session == "unknown" {
		t.Fatalf("listed session got %+v, %v", id, err)
	}
	if _, err := r.Identify(source.Unit{Key: "opencode-nope", Path: r.db}, nil); err == nil {
		t.Error("an unlisted session got a default identity")
	}
}

func TestABadMessageRowIsSkippedAndTheSessionStillReads(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_a", "", t0)
	f.message(t, "m1", "ses_a", t0+1000, reply(t0, 10, 2, 0, 0, 0, "", ""))
	f.message(t, "m2", "ses_a", t0+2000, reply(t0+1500, 20, 4, 0, 0, 0, "", ""))
	f.exec(t, `UPDATE message SET time_updated = 'later' WHERE id = 'm2'`)
	f.message(t, "m3", "ses_a", t0+3000, reply(t0+2500, 0, 0, 0, 0, 0, "1e15", `,"x":1`))
	f.exec(t, `UPDATE message SET data = replace(data, '"tokens":{"input":0', '"tokens":{"in":0') WHERE id = 'm3'`)
	rd := readAll(t, New(f.home), reading{})
	if len(rd.cursors) != 1 {
		t.Fatalf("a bad message row hid its session: %d units", len(rd.cursors))
	}
	for _, cur := range rd.cursors {
		if len(cur.Events) != 1 || cur.Skipped != 2 || cur.Lines != 3 {
			t.Fatalf("events %d skipped %d lines %d", len(cur.Events), cur.Skipped, cur.Lines)
		}
	}
}

func TestOnlyTheSessionAWriteTouchedLooksChanged(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_a", "", t0)
	f.session(t, "ses_b", "", t0)
	f.message(t, "a1", "ses_a", t0+1000, reply(t0, 10, 2, 0, 0, 0, "", ""))
	r := New(f.home)
	units := func() map[string]source.Unit {
		d, err := r.Discover()
		if err != nil || len(d.Units) != 2 {
			t.Fatalf("units %v, %v", d.Units, err)
		}
		out := map[string]source.Unit{}
		for _, u := range d.Units {
			out[u.Rel] = u
		}
		return out
	}
	before := units()
	if again := units(); fmt.Sprint(again) != fmt.Sprint(before) {
		t.Fatalf("an unchanged database listed other units: %v", again)
	}
	f.message(t, "b1", "ses_b", t0+5000, reply(t0+4000, 10, 2, 0, 0, 0, "", ""))
	after := units()
	a, b := dbFile+"#ses_a", dbFile+"#ses_b"
	if after[a].MtimeMs != before[a].MtimeMs || after[a].Size != before[a].Size {
		t.Errorf("a write to ses_b changed ses_a's marker: %+v -> %+v", before[a], after[a])
	}
	if after[b].MtimeMs == before[b].MtimeMs || after[b].Size == before[b].Size || after[b].Size < headerBytes {
		t.Errorf("ses_b's marker did not move: %+v -> %+v", before[b], after[b])
	}
}

func TestReadFailsWhenTheSchemaIsNoLongerKnown(t *testing.T) {
	f := newDB(t, v1Schema)
	f.session(t, "ses_a", "", t0)
	r := New(f.home)
	d, err := r.Discover()
	if err != nil || len(d.Units) != 1 {
		t.Fatalf("units %v, %v", d.Units, err)
	}
	f.exec(t, `ALTER TABLE message RENAME TO message_v9`)
	file, _, err := source.Open(r.Roots(), d.Units[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := r.Read(file, d.Units[0], source.Cursor{}, source.Options{}); err == nil {
		t.Fatal("an unknown schema read as an empty session")
	}
}
