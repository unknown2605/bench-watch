package aggregate

import (
	"errors"
	"hash/maphash"
	"slices"
	"time"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
)

// FullEvery is how long Live serves incremental runs before it rebuilds everything once and compares.
const FullEvery = time.Hour

// Live keeps the last build by day and replays only the shards a change can reach, keeping those days' rows; each
// such row gets the same additions in the same order as in BuildRows, so the two are equal.
type Live struct {
	contextBands, resultBands []Band
	days                      map[time.Time]*Rows
	digests                   map[time.Time]string
	shards                    map[string]shardInfo
	// Shards that share a (kind, ID) are joined; a hash collision only joins more, which costs time, not accuracy.
	parent []int32
	index  map[string]int32
	ids    map[uint64]int32
	// shared holds the IDs seen in more than one shard.
	shared  map[uint64]bool
	seed    maphash.Seed
	hot     map[string]hotShard
	builtAt time.Duration
	valid   bool
	// everBuilt is set by the first build, whatever its outcome.
	everBuilt bool
	// input holds each shard's hash as the last build saw it; nil before a build or after a failed one.
	input map[string]string
	// Drift counts the days where the last comparison found the incremental build different from a full one.
	Drift int
	// grown counts the changed shards whose touched days grownDays narrowed.
	grown int
}

type shardInfo struct {
	hash           string
	days           []time.Time
	lines, skipped int64
	events         int
	client         clients.ID
}

type hotShard struct {
	hash  string
	shard *scan.Shard
}

// maxHotEvents bounds the decoded shards kept between runs for the next replay.
const maxHotEvents = 200_000

func NewLive() *Live { return &Live{seed: maphash.MakeSeed()} }

// BuildKind says how a build replayed the shards.
type BuildKind int

const (
	// Incremental replayed only the shards a change can reach.
	Incremental BuildKind = iota
	// Scheduled replayed every shard because this Live never built or FullEvery passed since its last full build.
	Scheduled
	// Forced replayed every shard because an incremental build could not be trusted.
	Forced
)

// Build returns the rows of every shard in st, replaying only what changed when it can; at is a monotonic reading.
func (l *Live) Build(st *scan.Store, contextBands, resultBands []Band, at time.Duration) (rows *Rows, kind BuildKind,
	err error) {
	rows, kind, err = l.build(st, contextBands, resultBands, at)
	l.input = nil
	if err == nil {
		l.input = make(map[string]string, len(st.Entries))
		for key, entry := range st.Entries {
			l.input[key] = entry.ShardHash
		}
	}
	return rows, kind, err
}

// Rest drops the decoded shards kept for the next replay, which then reads them from the cache folder again.
func (l *Live) Rest() { l.hot = nil }

// Current says st holds exactly the shards the last build replayed, so building again would change nothing.
func (l *Live) Current(st *scan.Store) bool {
	if l.input == nil || len(l.input) != len(st.Entries) {
		return false
	}
	for key, entry := range st.Entries {
		if hash, ok := l.input[key]; !ok || hash != entry.ShardHash {
			return false
		}
	}
	return true
}

func (l *Live) build(st *scan.Store, contextBands, resultBands []Band, at time.Duration) (*Rows, BuildKind, error) {
	if len(contextBands) == 0 {
		contextBands = DefaultContextBands
	}
	if len(resultBands) == 0 {
		resultBands = DefaultResultBands
	}
	st.Cached = l.cached
	usable := l.valid && slices.Equal(bandKeys(l.contextBands), bandKeys(contextBands)) &&
		slices.Equal(bandKeys(l.resultBands), bandKeys(resultBands))
	kind := Forced
	if !l.everBuilt {
		kind = Scheduled
	}
	var incremental map[time.Time]string
	if usable {
		inc, ok, err := l.update(st)
		if err != nil {
			l.reset()
			return nil, Forced, err
		}
		if ok && at-l.builtAt < FullEvery {
			return inc, Incremental, nil
		}
		if ok {
			incremental, kind = inc.Digests(), Scheduled
		}
	}
	rows, err := l.rebuild(st, contextBands, resultBands, at)
	if l.Drift = 0; err == nil && incremental != nil {
		l.Drift = driftOf(incremental, rows.Digests())
	}
	return rows, kind, err
}

func driftOf(a, b map[time.Time]string) int {
	n := 0
	for d, x := range a {
		if b[d] != x {
			n++
		}
	}
	for d := range b {
		if _, ok := a[d]; !ok {
			n++
		}
	}
	return n
}

func bandKeys(bands []Band) []int64 {
	out := make([]int64, 0, 2*len(bands))
	for _, b := range bands {
		hi := int64(-1)
		if b.Max != nil {
			hi = *b.Max
		}
		out = append(out, b.Min, hi)
	}
	return out
}

func (l *Live) cached(key string, entry scan.Entry) *scan.Shard {
	if h, ok := l.hot[key]; ok && h.hash == entry.ShardHash {
		return h.shard
	}
	return nil
}

func (l *Live) reset() {
	l.days, l.digests, l.shards = map[time.Time]*Rows{}, map[time.Time]string{}, map[string]shardInfo{}
	l.parent, l.index, l.ids, l.hot = nil, map[string]int32{}, map[uint64]int32{}, map[string]hotShard{}
	l.shared, l.valid = map[uint64]bool{}, false
}

// rebuild replays every shard, with the same budget steps as before, and keeps the result when nothing was cut.
func (l *Live) rebuild(st *scan.Store, contextBands, resultBands []Band, at time.Duration) (*Rows, error) {
	var rows *Rows
	var err error
	l.everBuilt = true
	record := func(shard *scan.Shard, days []time.Time) {
		l.shards[shard.Key] = infoOf(shard, st.Entries[shard.Key].ShardHash, days)
		l.link(shard)
	}
	for budget := MaxRunItems; ; budget /= 4 {
		l.reset()
		rows, err = buildRows(st.Shards, budget, contextBands, resultBands, nil, record)
		if err == nil || !errors.Is(err, ErrTooManyRows) || budget == 0 {
			break
		}
	}
	if err != nil || rows.ItemsDropped > 0 || len(st.Failed) > 0 {
		l.reset()
		return rows, err
	}
	l.adopt(rows)
	l.contextBands, l.resultBands = contextBands, resultBands
	l.builtAt, l.valid = at, true
	return rows, nil
}

func infoOf(shard *scan.Shard, hash string, days []time.Time) shardInfo {
	return shardInfo{hash: hash, days: days, lines: shard.Lines, skipped: shard.Skipped, events: len(shard.Events),
		client: shard.Client}
}

func (l *Live) node(key string) int32 {
	i, ok := l.index[key]
	if !ok {
		i = int32(len(l.parent))
		l.index[key] = i
		l.parent = append(l.parent, i)
	}
	return i
}

func (l *Live) find(i int32) int32 {
	for l.parent[i] != i {
		l.parent[i] = l.parent[l.parent[i]]
		i = l.parent[i]
	}
	return i
}

func (l *Live) idOf(e extract.Event) uint64 {
	return maphash.String(l.seed, e.ID) ^ uint64(e.Kind+1)*0x9e3779b97f4a7c15
}

func (l *Live) link(shard *scan.Shard) {
	i := l.node(shard.Key)
	for _, e := range shard.Events {
		h := l.idOf(e)
		o, ok := l.ids[h]
		switch {
		case !ok:
			l.ids[h] = i
		case o != i:
			l.shared[h] = true
			if a, b := l.find(o), l.find(i); a != b {
				l.parent[a] = b
			}
		}
	}
}

// grownDays gives the days shard's growth from index from can change: its segment back to the last boundary it owns,
// and a sub-agent's run day; ok false when a grown event's ID is shared, which can change another shard's rows.
func (l *Live) grownDays(shard *scan.Shard, from int) (days []time.Time, ok bool) {
	events := shard.Events
	if from == 0 || from > len(events) {
		return nil, false
	}
	for _, e := range events[from:] {
		if l.shared[l.idOf(e)] {
			return nil, false
		}
	}
	start := 0
	for i := from - 1; i >= 0; i-- {
		if events[i].Kind == extract.Boundary && !l.shared[l.idOf(events[i])] {
			start = i
			break
		}
	}
	add := func(e extract.Event) {
		if day := dayOf(e.Ts); !slices.ContainsFunc(days, day.Equal) {
			days = append(days, day)
		}
	}
	for _, e := range events[start:] {
		add(e)
	}
	if shard.Scope == "sub" {
		for _, e := range events[:start] {
			add(e)
			if e.Kind == extract.Call && !l.shared[l.idOf(e)] {
				break
			}
		}
	}
	return days, true
}

// update replays the shards the changed ones can reach; ok false asks for a full replay instead. A shard counts as
// changed when its hash differs from the one last replayed, so an edit another process absorbed into the cache counts.
func (l *Live) update(st *scan.Store) (*Rows, bool, error) {
	var changedKeys, removedKeys []string
	for key, entry := range st.Entries {
		if info, ok := l.shards[key]; !ok || info.hash != entry.ShardHash {
			changedKeys = append(changedKeys, key)
		}
	}
	for key := range l.shards {
		if _, ok := st.Entries[key]; !ok {
			removedKeys = append(removedKeys, key)
		}
	}
	slices.Sort(changedKeys)
	rebuilt, failed := st.Rebuilt, len(st.Failed)
	changed := map[string]shardInfo{}
	loaded := map[string]*scan.Shard{}
	for _, key := range changedKeys {
		shard, err := st.Load(key)
		if err != nil {
			return nil, false, err
		}
		if shard == nil {
			return nil, false, nil
		}
		changed[key] = infoOf(shard, st.Entries[key].ShardHash, daysOf(shard))
		loaded[key] = shard
		l.link(shard)
	}
	touched := map[time.Time]bool{}
	roots := map[int32]bool{}
	for _, key := range changedKeys {
		if a, ok := st.AppendedTo(key); ok && l.shards[key].hash == a.After {
			if days, ok := l.grownDays(loaded[key], a.From); ok {
				l.grown++
				for _, d := range days {
					touched[d] = true
				}
				continue
			}
		}
		roots[l.find(l.node(key))] = true
	}
	for _, key := range removedKeys {
		roots[l.find(l.node(key))] = true
	}
	for key, info := range l.shards {
		if roots[l.find(l.node(key))] {
			for _, d := range info.days {
				touched[d] = true
			}
		}
	}
	for key, info := range changed {
		l.shards[key] = info
		if roots[l.find(l.node(key))] {
			for _, d := range info.days {
				touched[d] = true
			}
		}
	}
	for _, key := range removedKeys {
		delete(l.shards, key)
	}
	events := 0
	for _, info := range l.shards {
		events += info.events
	}
	if events > MaxRunItems {
		return nil, false, nil
	}
	needed := map[int32]bool{}
	for key, info := range l.shards {
		if slices.ContainsFunc(info.days, func(d time.Time) bool { return touched[d] }) {
			needed[l.find(l.node(key))] = true
		}
	}
	var replay []string
	for key := range l.shards {
		if needed[l.find(l.node(key))] {
			replay = append(replay, key)
		}
	}
	scan.OwnershipOrderOf(st.Entries, replay)
	hot, hotEvents, visited := map[string]hotShard{}, 0, 0
	rows, err := buildRows(st.ShardsOf(replay), MaxRunItems, l.contextBands, l.resultBands,
		func(d time.Time) bool { return touched[d] }, func(shard *scan.Shard, days []time.Time) {
			visited++
			l.shards[shard.Key] = infoOf(shard, st.Entries[shard.Key].ShardHash, days)
			if hotEvents+len(shard.Events) <= maxHotEvents {
				hot[shard.Key] = hotShard{hash: st.Entries[shard.Key].ShardHash, shard: shard}
				hotEvents += len(shard.Events)
			}
		})
	if errors.Is(err, ErrTooManyRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if visited != len(replay) || st.Rebuilt != rebuilt || len(st.Failed) != failed {
		return nil, false, nil
	}
	l.hot = hot
	for d := range touched {
		delete(l.days, d)
		delete(l.digests, d)
	}
	l.adopt(rows)
	out := l.assemble()
	if out.rowCount() > maxTableRows {
		return nil, false, nil
	}
	return out, true, nil
}

func daysOf(shard *scan.Shard) []time.Time {
	var days []time.Time
	for _, e := range shard.Events {
		day := dayOf(e.Ts)
		if n := len(days); n == 0 || !days[n-1].Equal(day) && !slices.ContainsFunc(days, day.Equal) {
			days = append(days, day)
		}
	}
	return days
}

// adopt stores rows by day, with each day's digest; the stored rows share no memory with rows.
func (l *Live) adopt(rows *Rows) {
	for day, r := range rows.byDay() {
		r.own()
		l.days[day] = r
		if d, ok := r.digest()[day]; ok {
			l.digests[day] = d
		}
	}
}

// assemble joins the stored days, oldest first, which is the order BuildRows sorts its rows in.
func (l *Live) assemble() *Rows {
	days := make([]time.Time, 0, len(l.days))
	for d := range l.days {
		days = append(days, d)
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	out := &Rows{Contributors: map[time.Time][]string{}, counts: map[time.Time]*dayCount{},
		digests: make(map[time.Time]string, len(l.digests))}
	for _, d := range days {
		r := l.days[d]
		out.Usage = append(out.Usage, r.Usage...)
		out.ContextBands = append(out.ContextBands, r.ContextBands...)
		out.Compactions = append(out.Compactions, r.Compactions...)
		out.Boundaries = append(out.Boundaries, r.Boundaries...)
		out.Sessions = append(out.Sessions, r.Sessions...)
		out.Subagents = append(out.Subagents, r.Subagents...)
		out.SubagentRuns = append(out.SubagentRuns, r.SubagentRuns...)
		out.Results = append(out.Results, r.Results...)
		out.ResultSizes = append(out.ResultSizes, r.ResultSizes...)
		out.TopResults = append(out.TopResults, r.TopResults...)
		out.Errors = append(out.Errors, r.Errors...)
		if c, ok := r.Contributors[d]; ok {
			out.Contributors[d] = c
		}
		if c, ok := r.counts[d]; ok {
			out.counts[d] = c
		}
	}
	for d, x := range l.digests {
		out.digests[d] = x
	}
	out.own()
	keys := make([]string, 0, len(l.shards))
	for key, info := range l.shards {
		keys = append(keys, key)
		out.Lines += info.lines
		out.Skipped += info.skipped
		out.addSkipped(info.client, info.skipped)
	}
	slices.Sort(keys)
	out.Keys, out.Transcripts = keys, len(keys)
	out.total()
	return out
}

// own gives r its own copy of every pointer and contributor list its rows hold, so a caller that changes them cannot
// reach the rows Live keeps; the per-day counts are unexported and only read.
func (r *Rows) own() {
	for i := range r.ContextBands {
		r.ContextBands[i].MaxTokens = clonePtr(r.ContextBands[i].MaxTokens)
	}
	for i := range r.Boundaries {
		r.Boundaries[i].TokensAfter = clonePtr(r.Boundaries[i].TokensAfter)
	}
	for i := range r.ResultSizes {
		r.ResultSizes[i].MaxTokens = clonePtr(r.ResultSizes[i].MaxTokens)
	}
	for d, c := range r.Contributors {
		r.Contributors[d] = slices.Clone(c)
	}
}

func clonePtr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func (r *Rows) rowCount() int {
	return len(r.Usage) + len(r.ContextBands) + len(r.Compactions) + len(r.Boundaries) + len(r.Sessions) +
		len(r.Subagents) + len(r.SubagentRuns) + len(r.Results) + len(r.ResultSizes) + len(r.Errors)
}

// byDay splits rows by day; every day with a row, a count or a contributor gets an entry.
func (r *Rows) byDay() map[time.Time]*Rows {
	out := map[time.Time]*Rows{}
	at := func(d time.Time) *Rows {
		x := out[d]
		if x == nil {
			x = &Rows{Contributors: map[time.Time][]string{}, counts: map[time.Time]*dayCount{}}
			out[d] = x
		}
		return x
	}
	for _, x := range r.Usage {
		g := at(x.Day)
		g.Usage = append(g.Usage, x)
	}
	for _, x := range r.ContextBands {
		g := at(x.Day)
		g.ContextBands = append(g.ContextBands, x)
	}
	for _, x := range r.Compactions {
		g := at(x.Day)
		g.Compactions = append(g.Compactions, x)
	}
	for _, x := range r.Boundaries {
		g := at(x.Day)
		g.Boundaries = append(g.Boundaries, x)
	}
	for _, x := range r.Sessions {
		g := at(x.Day)
		g.Sessions = append(g.Sessions, x)
	}
	for _, x := range r.Subagents {
		g := at(x.Day)
		g.Subagents = append(g.Subagents, x)
	}
	for _, x := range r.SubagentRuns {
		g := at(x.Day)
		g.SubagentRuns = append(g.SubagentRuns, x)
	}
	for _, x := range r.Results {
		g := at(x.Day)
		g.Results = append(g.Results, x)
	}
	for _, x := range r.ResultSizes {
		g := at(x.Day)
		g.ResultSizes = append(g.ResultSizes, x)
	}
	for _, x := range r.TopResults {
		g := at(x.Day)
		g.TopResults = append(g.TopResults, x)
	}
	for _, x := range r.Errors {
		g := at(x.Day)
		g.Errors = append(g.Errors, x)
	}
	for d, c := range r.Contributors {
		at(d).Contributors[d] = c
	}
	for d, c := range r.counts {
		at(d).counts[d] = c
	}
	return out
}
