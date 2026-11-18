package transcripts

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/codelog"
)

func callLine(id string, ts int64) string {
	return `{"type":"assistant","timestamp":"` + time.Unix(ts, 0).UTC().Format("2006-01-02T15:04:05.000Z") +
		`","message":{"id":"` + id + `","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":10,` +
		`"cache_creation_input_tokens":0,"cache_read_input_tokens":100,"output_tokens":5}}}` + "\n"
}

func appendLines(t *testing.T, file string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
}

func callsOn(t *testing.T, db DB, day int64) int64 {
	t.Helper()
	var n int64
	d := time.Unix(day, 0).In(time.Local).Format(time.DateOnly)
	q := "SELECT coalesce(sum(calls), 0)::bigint FROM usage_daily WHERE day = $1::date"
	if err := db.QueryRow(context.Background(), q, d).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Two sessions on one day, one transcript deleted; the stored day keeps both, with or without the record.
func TestDeletedTranscriptNeverShrinksAStoredDay(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "-Users-x-workspace-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(proj, "aaaaaaaa-0000-0000-0000-000000000000.jsonl")
	b := filepath.Join(proj, "bbbbbbbb-0000-0000-0000-000000000000.jsonl")
	past, today := noon(2), noon(0)
	appendLines(t, a, callLine("a1", past), callLine("a2", past+60))
	appendLines(t, b, callLine("b1", past+120))
	opt := Options{Projects: filepath.Join(dir, "projects"), Cache: filepath.Join(dir, "cache")}
	s := New(opt, conn)
	s.Tick(ctx)
	if st := s.Status(); st.DBErrorCode != "" || callsOn(t, conn, past) != 3 {
		t.Fatalf("first write: code %q, calls %d, want 3", st.DBErrorCode, callsOn(t, conn, past))
	}

	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	appendLines(t, b, callLine("b2", today))
	s.Tick(ctx)
	st := s.Status()
	if got := callsOn(t, conn, past); got != 3 || st.KeptDays != 1 || st.DBErrorCode != "" {
		t.Errorf("after the delete: calls %d (want 3), kept days %d (want 1), code %q", got, st.KeptDays, st.DBErrorCode)
	}
	if got := callsOn(t, conn, today); got != 1 {
		t.Errorf("today's new call: %d stored, want 1", got)
	}

	// Without the record of who contributed, the stored call count still guards the day.
	if err := os.Remove(filepath.Join(opt.Cache, "db-days.json")); err != nil {
		t.Fatal(err)
	}
	appendLines(t, b, callLine("b3", today+60))
	fresh := New(opt, conn)
	fresh.Tick(ctx)
	st = fresh.Status()
	if got := callsOn(t, conn, past); got != 3 || st.KeptDays != 1 {
		t.Errorf("fresh scanner: calls %d (want 3), kept days %d (want 1)", got, st.KeptDays)
	}
	if got := callsOn(t, conn, today); got != 2 {
		t.Errorf("fresh scanner, today: %d stored, want 2", got)
	}

	// The day is replaced again once its missing transcript is back.
	appendLines(t, a, callLine("a1", past), callLine("a2", past+60), callLine("a3", past+180))
	fresh.Tick(ctx)
	if got := callsOn(t, conn, past); got != 4 || fresh.Status().KeptDays != 0 {
		t.Errorf("transcript back: calls %d (want 4), kept days %d (want 0)", got, fresh.Status().KeptDays)
	}
}

// A day record that cannot be saved is reported and retried, not taken as stored.
func TestUnsavedDayRecordIsReported(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	projects, file := oneTranscript(t)
	cache := filepath.Join(t.TempDir(), "cache")
	s := New(Options{Projects: projects, Cache: cache}, conn)
	s.Tick(context.Background())
	if st := s.Status(); st.DBErrorCode != "" || st.DBPending {
		t.Fatalf("first write: code %q pending %v", st.DBErrorCode, st.DBPending)
	}
	record := filepath.Join(cache, "db-days.json")
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0o700); err != nil {
		t.Fatal(err)
	}
	appendLines(t, file, callLine("m2", time.Now().Unix()))
	s.Tick(context.Background())
	if st := s.Status(); st.DBErrorCode == "" || !st.DBPending {
		t.Errorf("unsaved record: code %q pending %v, want a code and pending", st.DBErrorCode, st.DBPending)
	}
}

// twoTranscripts stores a day from transcripts a (two calls) and b (one call) and returns the scanner options.
func twoTranscripts(t *testing.T) (opt Options, a, b string, past int64) {
	t.Helper()
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "-Users-x-workspace-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	a = filepath.Join(proj, "aaaaaaaa-0000-0000-0000-000000000000.jsonl")
	b = filepath.Join(proj, "bbbbbbbb-0000-0000-0000-000000000000.jsonl")
	past = noon(2)
	appendLines(t, a, callLine("a1", past), callLine("a2", past+60))
	appendLines(t, b, callLine("b1", past+120))
	return Options{Projects: filepath.Join(dir, "projects"), Cache: filepath.Join(dir, "cache")}, a, b, past
}

func TestKeptDayCountReachesTheLog(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	opt, a, b, past := twoTranscripts(t)
	s := New(opt, conn)
	s.Tick(ctx)
	codelog.Note("scan kept days", "")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	appendLines(t, b, callLine("b2", noon(0)))
	s.Tick(ctx)
	appendLines(t, a, callLine("a1", past), callLine("a2", past+60))
	s.Tick(ctx)
	var kept []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if _, code, ok := strings.Cut(l, "scan kept days: "); ok {
			kept = append(kept, code)
		}
	}
	if strings.Join(kept, ",") != "KEPT_1,ok" {
		t.Errorf("log %q, want the kept count once when it rises and once when it is back to zero", buf.String())
	}
}

// A day record from before contributors were kept has no keys, and only the stored call count guarded the day.
func TestDayWithoutContributorsGetsThemOnACleanRun(t *testing.T) {
	conn := testConn(t)
	emptyTranscripts(t, conn)
	t.Cleanup(func() { emptyTranscripts(t, conn) })
	ctx := context.Background()
	opt, a, b, past := twoTranscripts(t)
	New(opt, conn).Tick(ctx)
	record := filepath.Join(opt.Cache, "db-days.json")
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var f digestFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	for day, rec := range f.Days {
		f.Days[day] = dayRecord{Digest: rec.Digest}
	}
	if data, err = json.Marshal(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, data, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(opt, conn)
	s.Tick(ctx)
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	appendLines(t, b, callLine("b2", past+180), callLine("b3", past+240), callLine("b4", past+300))
	s.Tick(ctx)
	if got, st := callsOn(t, conn, past), s.Status(); got != 3 || st.KeptDays != 1 {
		t.Errorf("a lost transcript while another gained calls: %d calls stored (want 3), kept days %d (want 1)", got,
			st.KeptDays)
	}
}
