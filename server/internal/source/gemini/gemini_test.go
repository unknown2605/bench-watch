package gemini

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

const (
	sessA   = "abcdef01-0000-4000-8000-00000000000a"
	sessB   = "bbbbbbbb-0000-4000-8000-00000000000b"
	sessSub = "5ab5ab5a-0000-4000-8000-0000000000c1"
	qwenA   = "a0a0a0a0-0000-4000-8000-00000000000a"
	qwenB   = "b0b0b0b0-0000-4000-8000-00000000000b"
	qwenC   = "c0c0c0c0-0000-4000-8000-00000000000c"
)

// A cwd under the home folder gets a project label; the home folder itself is never read.
var qwenCwd = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "work", "proj")
}()

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func gemMeta(id, kind string) string {
	return `{"sessionId":"` + id + `","projectHash":"9f1c","startTime":"2026-05-19T07:00:00.000Z",` +
		`"lastUpdated":"2026-05-19T07:00:00.000Z","kind":"` + kind + `"}`
}

func gemUser(id string) string {
	return `{"id":"` + id + `","timestamp":"2026-05-19T07:00:01.000Z","type":"user","content":[{"text":"hi"}]}`
}

// gemReply is a "gemini" message as chatRecordingService writes it; tok is input, cached, output, thoughts, tool.
func gemReply(id, ts string, tok *[5]int64) string {
	tokens := "null"
	if tok != nil {
		t := *tok
		tokens = fmt.Sprintf(`{"input":%d,"cached":%d,"output":%d,"thoughts":%d,"tool":%d,"total":%d}`,
			t[0], t[1], t[2], t[3], t[4], t[0]+t[2]+t[3]+t[4])
	}
	return `{"id":"` + id + `","timestamp":"` + ts + `","type":"gemini","content":[{"text":"ok"}],` +
		`"model":"gemini-2.5-pro","tokens":` + tokens + `}`
}

var (
	g1 = [5]int64{1000, 400, 50, 20, 0}
	g2 = [5]int64{2000, 1500, 80, 0, 10}
	g3 = [5]int64{1200, 1000, 30, 0, 0}
	g4 = [5]int64{500, 0, 10, 0, 0}
	g5 = [5]int64{300, 100, 5, 5, 0}
	g6 = [5]int64{50, 0, 5, 0, 0}

	// A re-appends g1 once its tokens arrive, rewinds g2 away, relists g1 and g3 in a $set checkpoint and patches g3.
	gemA = lines(gemMeta(sessA, "main"), gemUser("u1"), gemReply("g1", "2026-05-19T07:00:02Z", nil),
		gemReply("g1", "2026-05-19T07:00:02Z", &g1), gemReply("g2", "2026-05-19T07:01:00Z", &g2),
		`{"$rewindTo":"g2"}`, gemReply("g3", "2026-05-19T07:02:00Z", &g3),
		`{"$set":{"messages":[`+gemReply("g1", "2026-05-19T07:00:02Z", &g1)+`,`+
			gemReply("g3", "2026-05-19T07:02:00Z", &g3)+`]}}`,
		`{"$patch":{"id":"g3","content":[{"text":"edited"}]}}`, `{"id":"broken",`)
	// B is a legacy whole-file session; resuming it migrated its messages into a .jsonl, which then went on.
	gemBLegacy = `{"sessionId":"` + sessB + `","projectHash":"9f1c","startTime":"2026-05-18T07:00:00Z",` +
		`"lastUpdated":"2026-05-18T07:00:00Z","messages":[` + gemUser("u4") + `,` +
		gemReply("g4", "2026-05-18T07:00:05Z", &g4) + `]}`
	gemBMigrated = lines(gemMeta(sessB, "main"), gemUser("u4"), gemReply("g4", "2026-05-18T07:00:05Z", &g4),
		gemReply("g5", "2026-05-20T08:00:00Z", &g5))
	gemSub = lines(gemMeta(sessSub, "subagent"), gemReply("g6", "2026-05-19T07:03:00Z", &g6))
)

func qwenRecord(uuid, session, typ, extra string) string {
	return `{"uuid":"` + uuid + `","parentUuid":null,"sessionId":"` + session +
		`","timestamp":"2026-05-19T09:00:00.000Z","type":"` + typ + `","cwd":"` + qwenCwd + `","version":"0.9.0"` +
		extra + `}`
}

func qwenReply(uuid, session string, prompt, cached, out, thoughts int64, extra string) string {
	return qwenRecord(uuid, session, "assistant", fmt.Sprintf(`,"model":"qwen3-coder-plus","usageMetadata":`+
		`{"promptTokenCount":%d,"cachedContentTokenCount":%d,"candidatesTokenCount":%d,"thoughtsTokenCount":%d,`+
		`"totalTokenCount":%d}`, prompt, cached, out, thoughts, prompt+out+thoughts)+extra)
}

var (
	// The ui_telemetry record repeats q1's usage and must not count.
	qwenALines = lines(qwenRecord("s1", qwenA, "system", `,"subtype":"ui_telemetry","systemPayload":`+
		`{"uiEvent":{"input_token_count":800,"output_token_count":40}}`),
		qwenRecord("q0", qwenA, "user", ""), qwenReply("q1", qwenA, 800, 300, 40, 10, ""),
		qwenReply("q2", qwenA, 900, 800, 20, 0, ""))
	// /branch copies q1 and q2 verbatim under a new sessionId and the same uuids.
	fork       = `,"forkedFrom":{"sessionId":"` + qwenA + `","messageUuid":"q2"}`
	qwenBLines = lines(qwenReply("q1", qwenB, 800, 300, 40, 10, fork), qwenReply("q2", qwenB, 900, 800, 20, 0, fork),
		qwenReply("q3", qwenB, 1000, 900, 15, 0, ""))
	qwenCLines = lines(qwenReply("q4", qwenC, 100, 0, 1, 0, ""))
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

type fixture struct {
	gemHome, qwenHome, chats string
	gem, qwen                *Reader
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{gemHome: t.TempDir(), qwenHome: t.TempDir()}
	f.chats = filepath.Join(f.gemHome, "tmp", "9f1c", "chats")
	write(t, filepath.Join(f.chats, "session-2026-05-19T07-00-abcdef01.jsonl"), gemA)
	write(t, filepath.Join(f.chats, "session-2026-05-18T07-00-bbbbbbbb.json"), gemBLegacy)
	write(t, filepath.Join(f.chats, "session-2026-05-18T07-00-bbbbbbbb.jsonl"), gemBMigrated)
	write(t, filepath.Join(f.chats, sessA, sessSub+".jsonl"), gemSub)
	qchats := filepath.Join(f.qwenHome, "projects", "-nowhere-proj", "chats")
	write(t, filepath.Join(qchats, qwenA+".jsonl"), qwenALines)
	write(t, filepath.Join(qchats, qwenB+".jsonl"), qwenBLines)
	write(t, filepath.Join(f.qwenHome, "tmp", "77aa", "chats", qwenC+".jsonl"), qwenCLines)
	f.gem, f.qwen = NewGemini(f.gemHome), NewQwen(f.qwenHome)
	return f
}

func readAll(t *testing.T, r *Reader, unit source.Unit, from source.Cursor) source.Cursor {
	t.Helper()
	f, _, err := source.Open(r.Roots(), unit.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cur, err := r.Read(f, unit, from, source.Options{MaxItems: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func unitOf(t *testing.T, r *Reader, name string) source.Unit {
	t.Helper()
	d, err := r.Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range d.Units {
		if filepath.Base(u.Path) == name {
			return u
		}
	}
	t.Fatalf("no unit %s", name)
	return source.Unit{}
}

func replay(units []source.Cursor) (n [5]int64, unique int) {
	best := map[string][5]int64{}
	for _, c := range units {
		for _, e := range c.Events {
			m := best[e.ID]
			for i := range m {
				m[i] = max(m[i], e.N[i])
			}
			best[e.ID] = m
		}
	}
	for _, m := range best {
		for i := range n {
			n[i] += m[i]
		}
	}
	return n, len(best)
}

func TestAReappendedRewoundAndCheckpointedGeminiSessionCountsEachReplyOnce(t *testing.T) {
	f := newFixture(t)
	cur := readAll(t, f.gem, unitOf(t, f.gem, "session-2026-05-19T07-00-abcdef01.jsonl"), source.Cursor{})
	// g1 600+400 cached / 50+20 thoughts, g2 (rewound, still billed) 2010-1500 / 80, g3 200 / 1000 / 30.
	want := [][5]int64{{600, 0, 0, 400, 70}, {510, 0, 0, 1500, 80}, {200, 0, 0, 1000, 30}}
	if len(cur.Events) != len(want) {
		t.Fatalf("events %d; want %d", len(cur.Events), len(want))
	}
	for i, e := range cur.Events {
		if e.N != want[i] || e.Kind != extract.Call || e.Text != "gemini-2.5-pro" {
			t.Errorf("event %d: %v %q; want %v", i, e.N, e.Text, want[i])
		}
	}
	if cur.Skipped != 1 {
		t.Errorf("skipped %d; want the one broken line", cur.Skipped)
	}
}

func TestAGeminiRecordingResumesAfterItsCutLineWithoutRecounting(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.chats, "session-2026-05-19T07-00-abcdef01.jsonl")
	cut := strings.Index(gemA, `{"$rewindTo"`) + 5
	write(t, path, gemA[:cut])
	first := readAll(t, f.gem, unitOf(t, f.gem, filepath.Base(path)), source.Cursor{})
	if len(first.Events) != 2 {
		t.Fatalf("first read %d events; want g1 and g2", len(first.Events))
	}
	write(t, path, gemA)
	next := readAll(t, f.gem, unitOf(t, f.gem, filepath.Base(path)), first)
	whole := readAll(t, f.gem, unitOf(t, f.gem, filepath.Base(path)), source.Cursor{})
	if n, u := replay([]source.Cursor{next}); u != 3 || n != [5]int64{1310, 0, 0, 2900, 180} {
		t.Fatalf("resumed %v over %d; want [1310 0 0 2900 180] over 3", n, u)
	}
	if next.Offset != whole.Offset || len(next.Events) != len(whole.Events) {
		t.Fatalf("resumed offset %d events %d; whole read %d %d", next.Offset, len(next.Events), whole.Offset,
			len(whole.Events))
	}
}

func TestALegacySessionAndItsMigratedCopyCountTheSharedReplyOnce(t *testing.T) {
	f := newFixture(t)
	legacy := readAll(t, f.gem, unitOf(t, f.gem, "session-2026-05-18T07-00-bbbbbbbb.json"), source.Cursor{})
	migrated := readAll(t, f.gem, unitOf(t, f.gem, "session-2026-05-18T07-00-bbbbbbbb.jsonl"), source.Cursor{})
	if len(legacy.Events) != 1 || len(migrated.Events) != 2 || legacy.Events[0].ID != migrated.Events[0].ID {
		t.Fatalf("legacy %d and migrated %d events do not share g4's ID", len(legacy.Events), len(migrated.Events))
	}
	if n, u := replay([]source.Cursor{legacy, migrated}); u != 2 || n != [5]int64{700, 0, 0, 100, 20} {
		t.Fatalf("legacy plus migrated %v over %d; want [700 0 0 100 20] over 2", n, u)
	}
	again := readAll(t, f.gem, unitOf(t, f.gem, "session-2026-05-18T07-00-bbbbbbbb.json"), legacy)
	if len(again.Events) != 1 || again.Events[0].N != legacy.Events[0].N {
		t.Fatalf("re-reading the legacy file changed its events: %v", again.Events)
	}
}

func TestAQwenBranchThatCopiesUUIDsAddsOnlyItsOwnReply(t *testing.T) {
	f := newFixture(t)
	a := readAll(t, f.qwen, unitOf(t, f.qwen, qwenA+".jsonl"), source.Cursor{})
	b := readAll(t, f.qwen, unitOf(t, f.qwen, qwenB+".jsonl"), source.Cursor{})
	if len(a.Events) != 2 || len(b.Events) != 3 || a.Events[0].ID != b.Events[0].ID || a.Events[1].ID != b.Events[1].ID {
		t.Fatalf("branch copies do not share the parent's IDs: %d %d", len(a.Events), len(b.Events))
	}
	if a.Events[0].N != [5]int64{500, 0, 0, 300, 50} || a.Events[0].Text != "qwen3-coder-plus" {
		t.Fatalf("q1 %v %q; want [500 0 0 300 50] qwen3-coder-plus", a.Events[0].N, a.Events[0].Text)
	}
	if n, u := replay([]source.Cursor{a, b}); u != 3 || n != [5]int64{700, 0, 0, 2000, 85} {
		t.Fatalf("parent plus branch %v over %d; want [700 0 0 2000 85] over 3", n, u)
	}
}

var hexSession = regexp.MustCompile(`^[0-9a-f]{8}$`)

func TestTheStoreCountsEveryReplyOnceAcrossBothClients(t *testing.T) {
	f := newFixture(t)
	cache, err := scan.NewCacheFor([]source.Reader{f.gem, f.qwen}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := cache.Update(nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Unreadable != 0 || len(st.Entries) != 7 {
		t.Fatalf("unreadable %d (%v), entries %d; want 0 and 7", st.Unreadable, st.FileErr, len(st.Entries))
	}
	byClient := map[source.Client][]source.Cursor{}
	sessions := map[string]bool{}
	if err := st.Shards(func(s *scan.Shard) error {
		if s.Scope != "main" || !hexSession.MatchString(s.Session) {
			t.Errorf("%s identity %q %q", s.Client, s.Session, s.Scope)
		}
		if (s.Client == "qwen-code") == (s.Project == "other") {
			t.Errorf("%s project %q", s.Client, s.Project)
		}
		sessions[string(s.Client)+" "+s.Session] = true
		byClient[s.Client] = append(byClient[s.Client], source.Cursor{Events: s.Events})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gemini-cli abcdef01", "gemini-cli bbbbbbbb", "qwen-code a0a0a0a0"} {
		if !sessions[want] {
			t.Errorf("no session %s in %v", want, sessions)
		}
	}
	// Gemini: g1..g6 with the sub-agent's g6 under its parent abcdef01. Qwen: q1..q4.
	if n, u := replay(byClient["gemini-cli"]); u != 6 || n != [5]int64{2060, 0, 0, 3000, 205} {
		t.Errorf("gemini %v over %d; want [2060 0 0 3000 205] over 6", n, u)
	}
	if n, u := replay(byClient["qwen-code"]); u != 4 || n != [5]int64{800, 0, 0, 2000, 86} {
		t.Errorf("qwen %v over %d; want [800 0 0 2000 86] over 4", n, u)
	}
}

var secretLike = regexp.MustCompile(`(?i)auth|cred|token|key|\.env`)

func TestTheReadersOpenOnlyChatRecordings(t *testing.T) {
	f := newFixture(t)
	outside := map[string][]string{
		f.gemHome:  {"oauth_creds.json", "google_accounts.json", "settings.json", ".env", "installation_id"},
		f.qwenHome: {"oauth_creds.json", "settings.json", ".env"},
	}
	readers := map[string]*Reader{f.gemHome: f.gem, f.qwenHome: f.qwen}
	for home, names := range outside {
		for _, name := range names {
			write(t, filepath.Join(home, name), "{}\n")
			if _, _, err := source.Open(readers[home].Roots(), filepath.Join(home, name)); !errors.Is(err,
				source.ErrOutsideRoots) {
				t.Errorf("%s: %v, want ErrOutsideRoots", name, err)
			}
		}
	}
	for _, rel := range []string{"tmp/9f1c/logs.json", "tmp/9f1c/shell_history", "tmp/9f1c/chats/notes.jsonl",
		"tmp/9f1c/chats/session-auth-token.jsonl", "tmp/9f1c/chats/session-x.txt", "tmp/9f1c/checkpoint-a.json",
		"tmp/9f1c/chats/" + sessA + "/deep/session-y.jsonl", "tmp/9f1c/chats/api_key.jsonl"} {
		write(t, filepath.Join(f.gemHome, rel), "{}\n")
	}
	for _, rel := range []string{"projects/-p/chats/oauth_creds.jsonl", "projects/-p/settings.json",
		"projects/-p/chats/sub/x.jsonl", "projects/.env.jsonl", "tmp/77aa/chats/notes.json"} {
		write(t, filepath.Join(f.qwenHome, rel), "{}\n")
	}
	if err := os.Symlink(filepath.Join(f.gemHome, "oauth_creds.json"),
		filepath.Join(f.chats, "session-2026-05-19T07-00-0000000f.jsonl")); err != nil {
		t.Fatal(err)
	}
	want := map[*Reader]int{f.gem: 5, f.qwen: 4}
	for r, n := range want {
		d, err := r.Discover()
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, u := range d.Units {
			names = append(names, u.Rel)
			if !secretLike.MatchString(filepath.Base(u.Path)) {
				continue
			}
			if _, _, err := source.Open(r.Roots(), u.Path); !errors.Is(err, source.ErrForbiddenName) {
				t.Errorf("%s: %v, want ErrForbiddenName", u.Rel, err)
			}
			if _, err := r.Identify(u, nil); !errors.Is(err, source.ErrForbiddenName) {
				t.Errorf("identify %s: %v, want ErrForbiddenName", u.Rel, err)
			}
		}
		if len(d.Units) != n {
			t.Errorf("%s units %v; want %d", r.Client(), names, n)
		}
	}
}

func readErr(t *testing.T, r *Reader, unit source.Unit) error {
	t.Helper()
	f, _, err := source.Open(r.Roots(), unit.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = r.Read(f, unit, source.Cursor{}, source.Options{MaxItems: 1000})
	return err
}

func TestRenamedTokenFieldsAndBadTimestampsAreSkipped(t *testing.T) {
	f := newFixture(t)
	name := "session-2026-05-20T07-00-abcdef01.jsonl"
	renamed := `{"id":"r1","timestamp":"2026-05-20T07:00:02.000Z","type":"gemini","model":"gemini-2.5-pro",` +
		`"tokens":{"in":5,"out":"2"}}`
	write(t, filepath.Join(f.chats, name), lines(gemMeta(sessA, "main"), renamed,
		gemReply("r2", "half past", &[5]int64{10, 0, 2, 0, 0})))
	cur := readAll(t, f.gem, unitOf(t, f.gem, name), source.Cursor{})
	if len(cur.Events) != 1 || cur.Skipped != 2 {
		t.Fatalf("events %+v skipped %d", cur.Events, cur.Skipped)
	}
	qname := qwenA[:8] + "-drift.jsonl"
	qbad := strings.Replace(qwenReply("q9", qwenA, 10, 0, 2, 0, ""), "2026-05-19T09:00:00.000Z", "soon", 1)
	qrenamed := qwenRecord("q8", qwenA, "assistant", `,"usageMetadata":{"prompt":10}`)
	write(t, filepath.Join(f.qwenHome, "projects", "-nowhere-proj", "chats", qname), lines(qbad, qrenamed))
	cur = readAll(t, f.qwen, unitOf(t, f.qwen, qname), source.Cursor{})
	if len(cur.Events) != 1 || cur.Skipped != 2 {
		t.Fatalf("qwen events %+v skipped %d", cur.Events, cur.Skipped)
	}
}

func TestALegacySessionThatDoesNotParseKeepsItsPreviousShard(t *testing.T) {
	f := newFixture(t)
	name := "session-2026-05-18T07-00-bbbbbbbb.json"
	write(t, filepath.Join(f.chats, name), gemBLegacy[:len(gemBLegacy)/2])
	if readErr(t, f.gem, unitOf(t, f.gem, name)) == nil {
		t.Fatal("a half-written legacy session read as an empty success")
	}
	write(t, filepath.Join(f.chats, name), `{"sessionId":"`+sessB+`","turns":[]}`)
	if err := readErr(t, f.gem, unitOf(t, f.gem, name)); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("a legacy session without messages: %v", err)
	}
	if maxLegacy != 16<<20 {
		t.Errorf("legacy cap %d", maxLegacy)
	}
}

func TestIdentifyFailsUntilTheFirstRecordParses(t *testing.T) {
	f := newFixture(t)
	name := "session-2026-05-21T07-00-cccccccc.jsonl"
	path := filepath.Join(f.chats, name)
	for _, content := range []string{"", gemMeta(sessA, "main")[:20]} {
		write(t, path, content)
		if _, err := f.gem.Identify(unitOf(t, f.gem, name), nil); err == nil {
			t.Fatalf("%q: a default identity was given", content)
		}
	}
	write(t, path, lines(gemMeta(sessA, "main")))
	if id, err := f.gem.Identify(unitOf(t, f.gem, name), nil); err != nil || id.Session != sessA[:8] {
		t.Fatalf("identity %+v, %v", id, err)
	}
}

func TestALargeRecordingWithNoKnownUsageIsUnreadable(t *testing.T) {
	f := newFixture(t)
	name := "session-2026-05-22T07-00-dddddddd.jsonl"
	var b strings.Builder
	b.WriteString(gemMeta(sessA, "main") + "\n")
	for b.Len() < source.FormatFloor {
		b.WriteString(`{"id":"x","timestamp":"2026-05-22T07:00:01.000Z","type":"model_v2","usage":{"in":1}}` + "\n")
	}
	write(t, filepath.Join(f.chats, name), b.String())
	if err := readErr(t, f.gem, unitOf(t, f.gem, name)); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("drifted recording: %v", err)
	}
}

func TestASmallRecordingIsUnreadableOnlyWhenNoRecordTypeIsKnown(t *testing.T) {
	f := newFixture(t)
	name := "session-2026-05-23T07-00-eeeeeeee.jsonl"
	path := filepath.Join(f.chats, name)
	drift := `{"id":"x","timestamp":"2026-05-23T07:00:01.000Z","type":"model_v2","usage":{"in":1}}`
	write(t, path, lines(gemMeta(sessA, "main"), drift))
	if err := readErr(t, f.gem, unitOf(t, f.gem, name)); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("a small drifted recording: %v", err)
	}
	write(t, path, lines(gemMeta(sessA, "main")))
	cur := readAll(t, f.gem, unitOf(t, f.gem, name), source.Cursor{})
	write(t, path, lines(gemMeta(sessA, "main"), gemUser("u9")))
	cur = readAll(t, f.gem, unitOf(t, f.gem, name), cur)
	write(t, path, lines(gemMeta(sessA, "main"), gemUser("u9"), drift))
	readAll(t, f.gem, unitOf(t, f.gem, name), cur)

	qname := qwenB + ".jsonl"
	qpath := filepath.Join(f.qwenHome, "projects", "-nowhere-proj", "chats", qname)
	write(t, qpath, lines(qwenRecord("q8", qwenB, "assistant_v2", "")))
	if err := readErr(t, f.qwen, unitOf(t, f.qwen, qname)); !errors.Is(err, source.ErrUnrecognised) {
		t.Fatalf("a small drifted qwen chat: %v", err)
	}
	write(t, qpath, lines(qwenRecord("q8", qwenB, "user", "")))
	readAll(t, f.qwen, unitOf(t, f.qwen, qname), source.Cursor{})
}
