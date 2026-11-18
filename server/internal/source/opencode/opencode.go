// Package opencode reads opencode's SQLite database (CM-00016): one unit per session, call events from assistant
// messages only. Built from upstream source (v1.14.48 and dev); no real row was seen.
package opencode

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	_ "modernc.org/sqlite"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

const dbFile = "opencode.db"

// headerBytes is the cursor offset of every read: the SQLite header, so the store re-reads a session from scratch
// when the header changes and otherwise continues from the time_updated watermark kept in LastTs.
const headerBytes = 100

// Generation is the schema an opencode database uses.
type Generation int

const (
	None Generation = iota
	// V1 is v1.x: session plus message(data JSON), as in v1.14.48.
	V1
	// V2 is dev: session_v2 plus session_message(type, data); session_v2's token columns are cumulative and unused.
	V2
)

type tables struct{ session, message, kind string }

var generations = map[Generation]tables{
	V1: {session: "session", message: "message"},
	V2: {session: "session_v2", message: "session_message", kind: "type"},
}

type Reader struct {
	home, db string
	mu       sync.Mutex
	ids      map[string]source.Identity
	// last is the previous full listing and the database state it was taken at, so an idle scan runs no query.
	last      []source.Unit
	lastState fileState
}

type fileState struct {
	dbMtime, walMtime float64
	dbSize, walSize   int64
}

var _ source.Reader = (*Reader)(nil)

// Home is ~/.local/share/opencode; XDG_DATA_HOME is not followed, and auth.json beside the database stays unopened.
func Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "opencode"), nil
}

func New(home string) *Reader {
	if a, err := filepath.Abs(home); err == nil {
		home = a
	}
	return &Reader{home: home, db: filepath.Join(home, dbFile), ids: map[string]source.Identity{}}
}

func (r *Reader) Client() source.Client { return clients.OpenCode }

func (r *Reader) Roots() []string { return []string{r.home} }

func (r *Reader) Detect() bool {
	st, err := os.Lstat(r.db)
	return err == nil && st.Mode().IsRegular()
}

// open is read-only and never immutable=1, which would skip a live -wal file's commits.
func (r *Reader) open() (*sql.DB, error) {
	if err := source.Check(r.Roots(), r.db); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: r.db, RawQuery: "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func has(cols map[string]bool, names ...string) bool {
	for _, n := range names {
		if !cols[n] {
			return false
		}
	}
	return true
}

// Detect reports the schema; V2 wins when both exist, as reading both would count copied messages twice.
func detect(db *sql.DB) (Generation, error) {
	for _, g := range []Generation{V2, V1} {
		t := generations[g]
		s, err := columns(db, t.session)
		if err != nil {
			return None, err
		}
		m, err := columns(db, t.message)
		if err != nil {
			return None, err
		}
		need := []string{"id", "session_id", "time_updated", "data"}
		if t.kind != "" {
			need = append(need, t.kind)
		}
		if has(s, "id", "directory", "parent_id", "time_created") && has(m, need...) {
			return g, nil
		}
	}
	return None, nil
}

func mtimeMs(path string) (float64, int64, bool) {
	var st syscall.Stat_t
	if syscall.Lstat(path, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return 0, 0, false
	}
	return scan.MsFromTimespec(st.Mtimespec), st.Size, true
}

// Discover lists one unit per session. A unit's MtimeMs and Size are its own messages' newest time_updated and
// headerBytes plus their count, so a write re-reads only the sessions it touched; BirthMs is the session's creation,
// so the oldest fork owns a copy. While the database and its -wal file are unchanged the last listing is reused.
func (r *Reader) Discover() (source.Discovery, error) {
	var d source.Discovery
	dbMtime, dbSize, ok := mtimeMs(r.db)
	if !ok {
		return d, nil
	}
	state := fileState{dbMtime: dbMtime, dbSize: dbSize}
	if walMtime, walSize, ok := mtimeMs(r.db + "-wal"); ok {
		state.walMtime, state.walSize = walMtime, walSize
	}
	r.mu.Lock()
	if r.last != nil && r.lastState == state {
		d.Units = append([]source.Unit(nil), r.last...)
		r.mu.Unlock()
		return d, nil
	}
	r.mu.Unlock()
	db, err := r.open()
	if err != nil {
		if errors.Is(err, source.ErrForbiddenName) || errors.Is(err, source.ErrOutsideRoots) {
			return d, err
		}
		d.UnreadableDirs++
		return d, nil
	}
	defer db.Close()
	g, err := detect(db)
	if err != nil || g == None {
		d.UnreadableDirs++
		return d, nil
	}
	ids := map[string]source.Identity{}
	complete := false
	defer func() {
		r.mu.Lock()
		for k, v := range ids {
			r.ids[k] = v
		}
		r.last, r.lastState = nil, fileState{}
		if complete {
			r.ids, r.last, r.lastState = ids, append([]source.Unit(nil), d.Units...), state
		}
		r.mu.Unlock()
	}()
	t := generations[g]
	rows, err := db.Query(fmt.Sprintf(`SELECT s.id, s.directory, s.parent_id, s.time_created, m.updated, m.n
		FROM %s s LEFT JOIN (SELECT session_id,
		CAST(max(CASE WHEN typeof(time_updated) IN ('integer', 'real') THEN time_updated END) AS INTEGER) AS updated,
		count(*) AS n FROM %s
		GROUP BY session_id) m ON m.session_id = s.id`, t.session, t.message))
	if err != nil {
		d.UnreadableDirs++
		return d, nil
	}
	defer rows.Close()
	for rows.Next() {
		var rawID, rawDir, rawParent, rawCreated, rawUpdated, rawCount any
		if err := rows.Scan(&rawID, &rawDir, &rawParent, &rawCreated, &rawUpdated, &rawCount); err != nil {
			d.UnreadableDirs++
			continue
		}
		id, ok := text(rawID)
		if !ok || id == "" {
			// Without its id the row's shard is unknown, so no session of this client may count as deleted.
			d.UnreadableDirs++
			continue
		}
		rel := dbFile + "#" + id
		key := source.Key(clients.OpenCode, rel)
		dir, okDir := text(rawDir)
		parent, okParent := text(rawParent)
		created, okCreated := integer(rawCreated)
		updated, okUpdated := integer(rawUpdated)
		n, okCount := integer(rawCount)
		if !okDir || !okParent || !okCreated || !okUpdated || !okCount {
			d.UnreadableKeys = append(d.UnreadableKeys, key)
			continue
		}
		root := id
		if parent != "" {
			root = parent
		}
		ids[key] = source.Identity{Project: projectOf(dir), Session: source.SessionLabel(root), Scope: "main"}
		d.Units = append(d.Units, source.Unit{Client: clients.OpenCode, Key: key, Path: r.db, Rel: rel,
			Size: headerBytes + n, MtimeMs: float64(updated), BirthMs: float64(created)})
	}
	if rows.Err() != nil {
		return source.Discovery{UnreadableDirs: d.UnreadableDirs + 1}, nil
	}
	complete = d.UnreadableDirs == 0 && len(d.UnreadableKeys) == 0
	return d, nil
}

// text reads a nullable text column; NULL is "" and any other type is unreadable.
func text(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", true
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return "", false
}

// integer reads a nullable integer column; NULL is 0, a non-integral or non-numeric value is unreadable.
func integer(v any) (int64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case int64:
		return x, true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	}
	return 0, false
}

func projectOf(dir string) string {
	if filepath.IsAbs(dir) {
		return labels.ProjectLabel(source.Dashed(dir), labels.HomePrefix())
	}
	return "other"
}

// Identify files a child session (a sub-agent) under its parent's session label with main scope.
func (r *Reader) Identify(unit source.Unit, previous *source.Identity) (source.Identity, error) {
	if err := source.Check(r.Roots(), unit.Path); err != nil {
		return source.Identity{}, err
	}
	if previous != nil {
		return *previous, nil
	}
	r.mu.Lock()
	id, ok := r.ids[unit.Key]
	r.mu.Unlock()
	if !ok {
		return source.Identity{}, fmt.Errorf("opencode: session %q was not listed", unit.Key)
	}
	return id, nil
}

func sessionOf(unit source.Unit) (string, bool) {
	prefix := dbFile + "#"
	if len(unit.Rel) <= len(prefix) || unit.Rel[:len(prefix)] != prefix {
		return "", false
	}
	return unit.Rel[len(prefix):], true
}

// Read takes the session's messages updated at or after the watermark second, so a row rewritten within that second
// is seen again; repeats only raise counts. f must be the database the store opened.
func (r *Reader) Read(f *os.File, unit source.Unit, from source.Cursor, opt source.Options) (source.Cursor, error) {
	session, ok := sessionOf(unit)
	if !ok || unit.Path != r.db {
		return source.Cursor{}, fmt.Errorf("opencode: unit %q is no session of the database", unit.Key)
	}
	opened, err := f.Stat()
	if err != nil {
		return source.Cursor{}, err
	}
	if named, err := os.Lstat(r.db); err != nil || !os.SameFile(opened, named) {
		return source.Cursor{}, errors.New("opencode: the database changed while it was opened")
	}
	db, err := r.open()
	if err != nil {
		return source.Cursor{}, err
	}
	defer db.Close()
	g, err := detect(db)
	if err != nil {
		return source.Cursor{}, err
	}
	if g == None {
		return source.Cursor{}, errors.New("opencode: no known schema")
	}
	c := source.NewCalls(from, opt.MaxItems)
	c.Cur.Offset = headerBytes
	t := generations[g]
	where := "session_id = ? AND time_updated >= ?"
	if t.kind != "" {
		where += " AND " + t.kind + " = 'assistant'"
	}
	rows, err := db.Query(fmt.Sprintf("SELECT time_updated, data FROM %s WHERE %s ORDER BY time_updated, id", t.message,
		where), session, from.LastTs*1000)
	if err != nil {
		return source.Cursor{}, err
	}
	defer rows.Close()
	watermark := from.LastTs
	for n := 0; rows.Next(); n++ {
		if n%1000 == 0 && opt.Tick != nil {
			if err := opt.Tick(); err != nil {
				return source.Cursor{}, err
			}
		}
		c.Cur.Lines++
		var rawUpdated, rawData any
		if err := rows.Scan(&rawUpdated, &rawData); err != nil {
			c.Cur.Skipped++
			continue
		}
		updated, okUpdated := integer(rawUpdated)
		data, okData := text(rawData)
		if !okUpdated || !okData {
			c.Cur.Skipped++
			continue
		}
		watermark = max(watermark, updated/1000)
		message, err := extract.Parse([]byte(data))
		if _, isMap := message.(map[string]any); err != nil || !isMap {
			c.Cur.Skipped++
			continue
		}
		assistant(c, message, t.kind != "")
	}
	if err := rows.Err(); err != nil {
		return source.Cursor{}, err
	}
	c.Cur.LastTs = watermark
	return c.Cur, nil
}

func number(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && !math.IsInf(f, 0) && f == f
}

// assistant files one finished assistant message, a compaction summary included. Its tokens are the message's own:
// step-finish parts repeat them. input excludes the cache; reasoning is billed as output, so it is added there.
func assistant(c *source.Calls, m any, typed bool) {
	if !typed && extract.Get(m, "role") != "assistant" {
		return
	}
	created, okCreated := number(extract.Get(extract.Get(m, "time"), "created"))
	_, completed := number(extract.Get(extract.Get(m, "time"), "completed"))
	tokens, okTokens := extract.Get(m, "tokens").(map[string]any)
	if !completed {
		return
	}
	if !okCreated || !okTokens {
		c.Cur.Skipped++
		return
	}
	provider, model := extract.Get(m, "providerID"), extract.Get(m, "modelID")
	if model == nil {
		provider, model = extract.Get(extract.Get(m, "model"), "providerID"), extract.Get(extract.Get(m, "model"), "id")
	}
	cache := extract.Get(tokens, "cache")
	var t source.Tokens
	n := [5]int64{t.Count(tokens["input"]), t.Count(extract.Get(cache, "write")), 0,
		t.Count(extract.Get(cache, "read")), t.Count(tokens["output"]) + t.Count(tokens["reasoning"])}
	// A fork clones messages under new ids, so the id is the content that a clone keeps.
	key := fmt.Sprintf("opencode:message:%v/%v|%s|%v", provider, model, strconv.FormatFloat(created, 'f', -1, 64), n)
	ts := min(extract.MaxTs, max(extract.MinTs, int64(math.Floor(created/1000))))
	label := labels.Model(clients.OpenCode, model)
	if cost, ok := number(extract.Get(m, "cost")); ok && cost >= 0 {
		c.AddRecorded(extract.HashID(key), ts, label, n, t, cost)
		return
	}
	c.Add(extract.HashID(key), ts, label, n, t)
}
