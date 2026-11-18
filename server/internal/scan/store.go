package scan

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/source"
)

const (
	metaMaxBytes = 1024 * 1024
	// About 5x the longest real line (1.6 MB; largest shard 5121 events). The worst parse shape measured, a list of
	// one-key objects filling the cap, peaks at 491 MB RSS (CM-00009).
	MaxLineBytes  = 8 * 1024 * 1024
	MaxShardItems = 500_000
	readChunk     = 1024 * 1024
	maxSafeInt    = 9007199254740991
)

var sessionFile = regexp.MustCompile(`^([0-9a-f]{8})-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var ErrLockLost = errors.New("ELOCKLOST")

func OutputStateFile(cacheDir string) string { return filepath.Join(cacheDir, "output.json") }

type transcript struct {
	full, rel, key string
	client         clients.ID
	size           int64
	mtimeMs        float64
	birthMs        float64
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// listing is what a walk keeps of one folder: its subfolders and its transcripts, named and keyed.
type listing struct {
	id    fileID
	dirs  []string
	files []transcript
}

// listTranscripts walks root; a folder whose identity matches its listing in seen is not read again, because adding,
// removing or renaming a name in it changes its mtime. Each transcript is still stat'ed, as appending does not.
func listTranscripts(root string, seen map[string]listing) (files []transcript, unreadableStats []string,
	unreadableDirs int, walked map[string]listing) {
	walked = map[string]listing{}
	pending := []string{root}
	for len(pending) > 0 {
		dir := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		list, ok := readListing(root, dir, seen[dir])
		if !ok {
			unreadableDirs++
			continue
		}
		if list.id != (fileID{}) {
			walked[dir] = list
		}
		pending = append(pending, list.dirs...)
		for _, file := range list.files {
			var st syscall.Stat_t
			if err := syscall.Lstat(file.full, &st); err != nil {
				if !errors.Is(err, syscall.ENOENT) {
					unreadableStats = append(unreadableStats, file.key)
				}
				continue
			}
			if st.Mode&syscall.S_IFMT == syscall.S_IFREG {
				file.size, file.mtimeMs = st.Size, MsFromTimespec(st.Mtimespec)
				file.birthMs = MsFromTimespec(st.Birthtimespec)
				files = append(files, file)
			}
		}
	}
	return files, unreadableStats, unreadableDirs, walked
}

// readListing lists dir, or reuses last when dir is unchanged since; ok is false for a folder that cannot be read,
// and a folder that is gone gives an empty listing.
func readListing(root, dir string, last listing) (listing, bool) {
	var st syscall.Stat_t
	statErr := syscall.Stat(dir, &st)
	id := fileID{dev: st.Dev, ino: st.Ino, size: st.Size, mtime: st.Mtimespec, ctm: st.Ctimespec}
	if statErr == nil && last.id != (fileID{}) && id == last.id {
		return last, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return listing{}, isNotExist(err)
	}
	list := listing{}
	if statErr == nil {
		list.id = id
	}
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		switch {
		case entry.IsDir():
			list.dirs = append(list.dirs, full)
		case entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl"):
			rel, _ := filepath.Rel(root, full)
			list.files = append(list.files, transcript{full: full, rel: rel, key: source.Key(clients.ClaudeCode, rel), client: clients.ClaudeCode})
		}
	}
	return list, true
}

// Node decodes file names as UTF-8 with U+FFFD for bad bytes before any label sees them.
func identity(rel string) (project, session, scope string) {
	parts := strings.Split(extract.DecodeUTF8([]byte(rel)), string(filepath.Separator))
	sub := false
	for _, p := range parts {
		if p == "subagents" {
			sub = true
		}
	}
	name := strings.TrimSuffix(parts[len(parts)-1], ".jsonl")
	scope = "main"
	if sub {
		name, scope = parts[1], "sub"
	}
	session = "unknown"
	if m := sessionFile.FindStringSubmatch(name); m != nil {
		session = m[1]
	}
	return labels.ProjectLabel(parts[0], labels.HomePrefix()), session, scope
}

// OwnershipOrder sorts by first timestamp; forks copy lines with their original timestamps, so the earliest-born file
// breaks ties.
func OwnershipOrder(entries map[string]Entry) []string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	return OwnershipOrderOf(entries, keys)
}

// OwnershipOrderOf sorts keys of entries in place by the same order as OwnershipOrder.
func OwnershipOrderOf(entries map[string]Entry, keys []string) []string {
	sort.Slice(keys, func(i, j int) bool {
		x, y := entries[keys[i]], entries[keys[j]]
		switch {
		case x.FirstTs != y.FirstTs:
			return x.FirstTs < y.FirstTs
		case x.BirthMs != y.BirthMs:
			return x.BirthMs < y.BirthMs
		case x.Sub != y.Sub:
			return !x.Sub
		}
		return keys[i] < keys[j]
	})
	return keys
}

type Store struct {
	root, shardDir, indexFile, outputState string
	tick                                   func() error
	cache                                  *Cache
	Entries                                map[string]Entry
	filesByKey                             map[string]transcript
	Failed, verified                       map[string]bool
	dirty                                  bool
	Changed, Removed, Rebuilt, Unreadable  int
	BytesRead                              int64
	// ShardReads counts shard files opened for their content, which an idle run must leave at zero.
	ShardReads int
	// FileErr is the first error this run that left a transcript unread; it is counted in Unreadable.
	FileErr error
	// Rereads counts transcripts read again from the start because their seen set was not in memory; RereadBytes is
	// what that read.
	Rereads     int
	RereadBytes int64
	// Cached may supply the decoded shard of an entry before the disk is read; it must hold that entry's content.
	Cached func(key string, entry Entry) *Shard
	// appended holds the shards this run read on from their previous content.
	appended map[string]Appended
}

// Appended says a shard is the one whose hash is After with events added or raised from index From on; every
// event before From is unchanged and none was removed.
type Appended struct {
	After string
	From  int
}

// AppendedTo reports how the shard of key grew this run, when it was read on from the content its old hash names.
func (s *Store) AppendedTo(key string) (Appended, bool) {
	a, ok := s.appended[key]
	return a, ok
}

func (s *Store) Checked() int { return len(s.filesByKey) }

func (s *Store) shardPath(key string) string { return filepath.Join(s.shardDir, key+".json") }

func (s *Store) loadShard(key, hash string, checkHash bool) *Shard {
	s.ShardReads++
	data, err := os.ReadFile(s.shardPath(key))
	if err != nil || (checkHash && extract.HashKey(data) != hash) {
		return nil
	}
	return decodeShard(data, key)
}

// seenFor returns the seen set of the bytes previous covers, when memory holds it and the transcript still has the
// same bytes just before that offset. It returns nil when the transcript has to be read from the start instead.
func (s *Store) seenFor(f *os.File, file transcript, previous *Shard) (set *seenSet, held bool, err error) {
	mem := s.cache.seen
	if previous == nil || previous.Offset == 0 || file.size < previous.Offset {
		mem.forget(file.key)
		return nil, false, nil
	}
	held = mem.sets[file.key] != nil
	set, err = mem.take(f, file.key, previous.Offset)
	return set, held, err
}

// grownFrom is Appended.From for a shard read from the start: the first event that differs from previous, or -1 when
// shard is not previous with events added or raised.
func grownFrom(previous, shard *Shard) int {
	sameAgent := (previous.AgentType == nil) == (shard.AgentType == nil) &&
		(previous.AgentType == nil || *previous.AgentType == *shard.AgentType)
	if previous.Project != shard.Project || previous.Session != shard.Session || previous.Scope != shard.Scope ||
		!sameAgent || len(shard.Events) < len(previous.Events) {
		return -1
	}
	for i, e := range previous.Events {
		if shard.Events[i] != e {
			return i
		}
	}
	return len(previous.Events)
}

// read returns the updated shard, or nil for a file that vanished. Other read errors keep the previous shard. from
// is Appended.From when the shard is previous grown, else -1. set is the seen set to keep once shard is stored.
func (s *Store) read(file transcript, previous *Shard) (shard *Shard, set *seenSet, from int, err error) {
	reader := s.cache.readers[file.client]
	if reader == nil {
		return nil, nil, -1, fmt.Errorf("no reader for client %q", file.client)
	}
	unit := file.unit()
	f, _, err := source.Open(reader.Roots(), file.full)
	if err != nil {
		if isNotExist(err) {
			return nil, nil, -1, nil
		}
		return nil, nil, -1, err
	}
	defer f.Close()
	set, held, err := s.seenFor(f, file, previous)
	if err != nil {
		return nil, nil, -1, err
	}
	// Nothing stored proves that the file still starts with what previous holds, so without the set it is read whole.
	shard = previous
	var again int64
	if set == nil {
		id, err := reader.Identify(unit, nil)
		if err != nil {
			return nil, nil, -1, err
		}
		shard, set = &Shard{Key: file.key, Client: file.client, Pending: map[string]string{}}, s.cache.seen.fresh()
		shard.setIdentity(id)
		if previous != nil && !held && file.size >= previous.Offset {
			again = previous.Offset
		}
	}
	continued := shard == previous
	if continued {
		was := shard.identity()
		id, err := reader.Identify(unit, &was)
		if err != nil {
			return nil, nil, -1, err
		}
		continued = id.Equal(was)
		shard.setIdentity(id)
	}
	before := slices.Clone(shard.Events)
	mem := s.cache.seen
	cur, err := reader.Read(f, unit, shard.cursor(), source.Options{Tick: s.tick, MaxItems: s.cache.maxItems,
		Repeat: func(label string, content any) (bool, error) { return mem.repeat(set, label, content) }})
	var tail seenKey
	if err == nil {
		tail, err = mem.tail(f, cur.Offset)
	}
	if err != nil {
		mem.drop(set)
		return nil, nil, -1, err
	}
	n := cur.Offset - shard.Offset
	set.offset, set.tail = cur.Offset, tail
	shard.Offset, shard.LastTs, shard.Lines, shard.Skipped = cur.Offset, cur.LastTs, cur.Lines, cur.Skipped
	shard.Pending, shard.Events = cur.Pending, cur.Events
	shard.Size, shard.MtimeMs = file.size, file.mtimeMs
	if again = min(again, n); again > 0 {
		s.Rereads++
		s.RereadBytes += again
	}
	s.BytesRead += n - again
	if shard != previous {
		if previous == nil {
			return shard, set, -1, nil
		}
		return shard, set, grownFrom(previous, shard), nil
	}
	if !continued {
		return shard, set, -1, nil
	}
	// Reading on only appends events, but a call line repeating an earlier ID raises that event's counts or cost.
	for i, e := range before {
		if shard.Events[i] != e {
			return shard, set, i, nil
		}
	}
	return shard, set, len(before), nil
}

func (file transcript) unit() source.Unit {
	return source.Unit{Client: file.client, Key: file.key, Path: file.full, Rel: file.rel, Size: file.size,
		MtimeMs: file.mtimeMs, BirthMs: file.birthMs}
}

func (s *Shard) identity() source.Identity {
	return source.Identity{Project: s.Project, Session: s.Session, Scope: s.Scope, AgentType: s.AgentType}
}

func (s *Shard) setIdentity(id source.Identity) {
	s.Project, s.Session, s.Scope, s.AgentType = id.Project, id.Session, id.Scope, id.AgentType
}

func (s *Shard) cursor() source.Cursor {
	return source.Cursor{Offset: s.Offset, LastTs: s.LastTs, Lines: s.Lines, Skipped: s.Skipped, Pending: s.Pending,
		Events: s.Events}
}

// discover lists every reader's units; blind holds the clients with a folder that could not be listed.
func (s *Store) discover() (files []transcript, unreadableStats []string, blind map[clients.ID]bool, err error) {
	blind = map[clients.ID]bool{}
	for _, reader := range s.cache.order {
		d, err := reader.Discover()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", reader.Client(), err)
		}
		if d.UnreadableDirs > 0 {
			blind[reader.Client()] = true
		}
		s.Unreadable += d.UnreadableDirs
		unreadableStats = append(unreadableStats, d.UnreadableKeys...)
		for _, u := range d.Units {
			if u.Client != reader.Client() || !strings.HasPrefix(u.Key, string(u.Client)+"-") {
				return nil, nil, nil, fmt.Errorf("%s: unit %q is not keyed for its client", reader.Client(), u.Key)
			}
			files = append(files, transcript{full: u.Path, rel: u.Rel, key: u.Key, client: u.Client, size: u.Size,
				mtimeMs: u.MtimeMs, birthMs: u.BirthMs})
		}
	}
	return files, unreadableStats, blind, nil
}

func clientOfKey(key string) clients.ID {
	for _, c := range clients.All() {
		if strings.HasPrefix(key, string(c)+"-") {
			return c
		}
	}
	return ""
}

func (s *Store) commit(file transcript, shard *Shard) (Entry, error) {
	data := encodeShard(shard)
	if err := WriteAtomic(s.shardPath(file.key), data, 0o600); err != nil {
		return Entry{}, err
	}
	s.verified[file.key] = true
	firstTs := float64(maxSafeInt)
	if len(shard.Events) > 0 {
		firstTs = float64(shard.Events[0].Ts)
	}
	return Entry{
		Size: file.size, MtimeMs: file.mtimeMs, BirthMs: file.birthMs, FirstTs: firstTs, Sub: shard.Scope == "sub",
		ShardBytes: int64(len(data)), ShardHash: extract.HashKey(data),
	}, nil
}

func (s *Store) readAndCommit(file transcript, previous *Shard) (*Entry, *Shard, int, error) {
	shard, set, from, err := s.read(file, previous)
	if err != nil || shard == nil {
		return nil, nil, -1, err
	}
	entry, err := s.commit(file, shard)
	if err != nil {
		s.cache.seen.drop(set)
		return nil, nil, -1, err
	}
	s.cache.seen.keep(file.key, set, file.mtimeMs)
	return &entry, shard, from, nil
}

func (s *Store) update() error {
	known, trusted := s.cache.index(s.indexFile, s.shardDir)
	s.Unreadable = 0
	files, unreadableStats, blind, err := s.discover()
	if err != nil {
		return err
	}
	next := map[string]Entry{}
	s.Unreadable += len(unreadableStats)
	for _, key := range unreadableStats {
		if entry, ok := known[key]; ok {
			next[key] = entry
		}
	}
	for _, file := range files {
		if err := s.tick(); err != nil {
			return err
		}
		entry, hasEntry := known[file.key]
		s.filesByKey[file.key] = file
		if hasEntry && entry.Size == file.size && entry.MtimeMs == file.mtimeMs && entry.BirthMs == file.birthMs {
			if trusted || sizeOf(s.shardPath(file.key)) == entry.ShardBytes {
				next[file.key] = entry
				continue
			}
		}
		// Without an index entry the shard cannot be checked, so the transcript is read from the start.
		var previous *Shard
		if hasEntry {
			previous = s.loadShard(file.key, entry.ShardHash, true)
		}
		fresh, _, from, err := s.readAndCommit(file, previous)
		// A new birth time is another file: its place in the ownership order may differ, which no event shows.
		if fresh != nil && from >= 0 && fresh.BirthMs == entry.BirthMs {
			s.appended[file.key] = Appended{After: entry.ShardHash, From: from}
		}
		switch {
		case errors.Is(err, ErrLockLost):
			return err
		case err != nil:
			s.Unreadable++
			if s.FileErr == nil {
				s.FileErr = err
			}
			if hasEntry {
				next[file.key] = entry
			}
		case fresh != nil:
			next[file.key] = *fresh
			s.Changed++
		}
	}
	for key, entry := range known {
		if _, ok := next[key]; ok {
			continue
		}
		// An unreadable folder hides its files, so what is unseen is not known to be deleted.
		if blind[clientOfKey(key)] {
			next[key] = entry
			continue
		}
		s.Removed++
	}
	s.Entries = next
	if s.Changed > 0 || s.Removed > 0 {
		s.dirty = true
	}
	if err := s.Save(); err != nil {
		return err
	}
	if !trusted || s.Changed > 0 || s.Removed > 0 {
		s.sweep()
	}
	s.cache.verified(s.shardDir)
	return nil
}

func sizeOf(file string) int64 {
	st, err := os.Stat(file)
	if err != nil {
		return -1
	}
	return st.Size()
}

// sweep removes leftovers of a crash, or shards of an older format.
func (s *Store) sweep() {
	entries, err := os.ReadDir(s.shardDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if key, ok := strings.CutSuffix(name, ".json"); ok {
			if _, kept := s.Entries[key]; kept {
				continue
			}
		}
		_ = os.Remove(filepath.Join(s.shardDir, name))
	}
}

func (s *Store) Save() error {
	if !s.dirty {
		return nil
	}
	// Order matters: once the index holds a change, a kill must not leave a state that calls the old output current.
	if err := os.Remove(s.outputState); err != nil && !isNotExist(err) {
		return err
	}
	keys := make([]string, 0, len(s.Entries))
	for k := range s.Entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if err := WriteAtomic(s.indexFile, encodeIndex(keys, s.Entries), 0o600); err != nil {
		s.cache.forget()
		return err
	}
	s.dirty = false
	s.cache.wrote(s.indexFile, s.Entries)
	return nil
}

// Load skips the hash on a second pass, because the first one already checked this shard in this run.
func (s *Store) Load(key string) (*Shard, error) {
	entry := s.Entries[key]
	if s.Cached != nil {
		if shard := s.Cached(key, entry); shard != nil {
			return shard, nil
		}
	}
	shard := s.loadShard(key, entry.ShardHash, !s.verified[key])
	if shard != nil && shard.Size == entry.Size && shard.MtimeMs == entry.MtimeMs {
		s.verified[key] = true
		return shard, nil
	}
	if s.Failed[key] {
		return nil, nil
	}
	if file, ok := s.filesByKey[key]; ok {
		fresh, rebuilt, _, err := s.readAndCommit(file, nil)
		if errors.Is(err, ErrLockLost) {
			return nil, err
		}
		if fresh != nil {
			s.Entries[key] = *fresh
			s.dirty = true
			s.Rebuilt++
			return rebuilt, nil
		}
	}
	if shard == nil {
		s.Failed[key] = true
	}
	return shard, nil
}

// Shards visits every shard in ownership order: the earliest transcript owns a shared message ID.
func (s *Store) Shards(visit func(*Shard) error) error {
	return s.visit(OwnershipOrder(s.Entries), visit)
}

// ShardsOf visits the shards of keys, which must be in ownership order; a key without an entry is skipped.
func (s *Store) ShardsOf(keys []string) func(visit func(*Shard) error) error {
	return func(visit func(*Shard) error) error { return s.visit(keys, visit) }
}

func (s *Store) visit(keys []string, visit func(*Shard) error) error {
	for _, key := range keys {
		if _, ok := s.Entries[key]; !ok {
			continue
		}
		if err := s.tick(); err != nil {
			return err
		}
		shard, err := s.Load(key)
		if err != nil {
			return err
		}
		if shard != nil {
			if err := visit(shard); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdateShards brings the cache up to date; tick runs often and may return ErrLockLost to stop the run.
func UpdateShards(root, cacheDir string, tick func() error) (*Store, error) {
	return NewCache(root, cacheDir).Update(tick)
}

// Cache remembers between runs of one process what the last run read and checked, so a run where nothing changed
// neither decodes the index again nor stats every shard. A shard edited in place still fails its hash when loaded.
type Cache struct {
	dir     string
	readers map[clients.ID]source.Reader
	order   []source.Reader
	known   map[string]Entry
	indexID fileID
	// shardsID is the shard folder as it was when every entry's shard was last found with its size.
	shardsID fileID
	seen     *seen
	maxItems int
}

// fileID changes whenever a file is replaced (WriteAtomic renames a new inode in) or a folder gains or loses a name.
type fileID struct {
	dev        int32
	ino        uint64
	size       int64
	mtime, ctm syscall.Timespec
}

func idOf(file string) (fileID, bool) {
	var st syscall.Stat_t
	if syscall.Lstat(file, &st) != nil {
		return fileID{}, false
	}
	return fileID{dev: st.Dev, ino: st.Ino, size: st.Size, mtime: st.Mtimespec, ctm: st.Ctimespec}, true
}

// NewCache reads Claude Code's transcripts under root.
func NewCache(root, cacheDir string) *Cache {
	cache, err := NewCacheFor([]source.Reader{NewClaudeReader(root)}, cacheDir)
	if err != nil {
		panic(err)
	}
	return cache
}

// NewCacheFor reads with one reader per client, listed in the order they are discovered.
func NewCacheFor(readers []source.Reader, cacheDir string) (*Cache, error) {
	c := &Cache{dir: cacheDir, readers: map[clients.ID]source.Reader{}, seen: newSeen(), maxItems: MaxShardItems}
	for _, r := range readers {
		if !r.Client().Valid() || c.readers[r.Client()] != nil {
			return nil, fmt.Errorf("reader for client %q is invalid or given twice", r.Client())
		}
		c.readers[r.Client()] = r
		c.order = append(c.order, r)
	}
	return c, nil
}

// Clients are the clients whose records exist on this machine.
func (c *Cache) Clients() []clients.ID {
	var out []clients.ID
	for _, r := range c.order {
		if r.Detect() {
			out = append(out, r.Client())
		}
	}
	return out
}

// index returns the decoded index, which the caller must not change; trusted says no shard file changed since the
// last run found them all, so their sizes need no new check.
func (c *Cache) index(indexFile, shardDir string) (map[string]Entry, bool) {
	id, ok := idOf(indexFile)
	if ok && c.known != nil && id == c.indexID {
		dir, ok := idOf(shardDir)
		return c.known, ok && dir == c.shardsID
	}
	c.forget()
	known := map[string]Entry{}
	if data, err := os.ReadFile(indexFile); err == nil {
		known = decodeIndex(data)
		if after, same := idOf(indexFile); ok && same && after == id {
			c.known, c.indexID = known, id
		}
	}
	return known, false
}

func (c *Cache) wrote(indexFile string, entries map[string]Entry) {
	c.forget()
	if id, ok := idOf(indexFile); ok {
		c.known, c.indexID = maps.Clone(entries), id
	}
}

func (c *Cache) verified(shardDir string) {
	if id, ok := idOf(shardDir); ok && c.known != nil {
		c.shardsID = id
	}
}

func (c *Cache) forget() { c.known, c.indexID, c.shardsID = nil, fileID{}, fileID{} }

// Update brings the cache up to date; tick runs often and may return ErrLockLost to stop the run.
func (c *Cache) Update(tick func() error) (*Store, error) {
	if tick == nil {
		tick = func() error { return nil }
	}
	s := &Store{
		shardDir: filepath.Join(c.dir, "shards"), indexFile: filepath.Join(c.dir, "index.json"),
		outputState: OutputStateFile(c.dir), tick: tick, cache: c, Entries: map[string]Entry{},
		filesByKey: map[string]transcript{}, Failed: map[string]bool{}, verified: map[string]bool{},
		appended: map[string]Appended{},
	}
	if err := os.MkdirAll(s.shardDir, 0o700); err != nil {
		return nil, err
	}
	if err := s.update(); err != nil {
		return s, err
	}
	c.seen.expire(func(key string) (float64, bool) {
		file, ok := s.filesByKey[key]
		return file.mtimeMs, ok
	})
	return s, nil
}
