package aggregate

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/pricing"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/store"
)

const (
	// TopResultsPerDay bounds tool_result_top: the API shows at most 20 results.
	TopResultsPerDay = 20
	// Results tracked one by one per segment for tool_result_top; the per-tool totals count every result.
	maxTrackedResults = 4096
)

// Band is a token band [Min, Max); Max nil is open.
type Band struct {
	Min int64
	Max *int64
}

func bandsOf(bounds ...int64) []Band {
	out := make([]Band, len(bounds))
	for i, b := range bounds {
		out[i].Min = b
		if i+1 < len(bounds) {
			out[i].Max = &bounds[i+1]
		}
	}
	return out
}

// The migration's token_bands rows, used when the table cannot be read.
var (
	DefaultContextBands = bandsOf(0, 100_000, 200_000, 400_000, 700_000)
	DefaultResultBands  = bandsOf(0, 1000, 5000, 20_000, 50_000)
)

func bandFor(bands []Band, v int64) int64 {
	pick := int64(0)
	for _, b := range bands {
		if v >= b.Min && (b.Max == nil || v < *b.Max) {
			return b.Min
		}
		if v >= b.Min {
			pick = b.Min
		}
	}
	return pick
}

// bandMax returns a copy of the band's bound, so a row never points into the bands themselves.
func bandMax(bands []Band, lo int64) *int64 {
	for _, b := range bands {
		if b.Min == lo {
			return clonePtr(b.Max)
		}
	}
	return nil
}

// The schema's label checks; a label that fails becomes the sentinel the schema accepts.
var (
	projectLabel = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]{0,119}|scratch \(tmp\))$`)
	agentLabel   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,59}$`)
	toolPlain    = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9_-]{0,59}|Read: (?:whole|ranged|image)|` +
		`mcp__[A-Za-z0-9_-]{1,60}__[A-Za-z0-9_-]{1,60})$`)
	sessionLabel = regexp.MustCompile(`^(?:[0-9a-f]{8}|unknown)$`)
	boundaryKey  = regexp.MustCompile(`^(?:[0-9a-f]{16}|[1-9][0-9]{8,12})$`)
)

var errorClasses = map[string]store.ErrorClass{
	"blocked by hook": store.ErrorClassBlockedbyhook, "denied": store.ErrorClassDenied,
	"timeout": store.ErrorClassTimeout, "non-zero exit": store.ErrorClassNonZeroexit,
	"not found": store.ErrorClassNotfound, "other": store.ErrorClassOther,
}

func label(re *regexp.Regexp, v, sentinel string) string {
	if len(v) <= 125 && re.MatchString(v) {
		return v
	}
	return sentinel
}

// isHash16 is /^[0-9a-f]{16}$/ without the regexp, which the replay would run once per tool result.
func isHash16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// The schema's Bash list is labels.Families plus "other" and "shell loop"; the database test checks they agree.
func toolOf(v string) string {
	if toolPlain.MatchString(v) {
		return v
	}
	if family, ok := strings.CutPrefix(v, "Bash: "); ok &&
		(labels.Families[family] || family == "other" || family == "shell loop") {
		return v
	}
	return "other"
}

// Rows holds every transcript table, keyed by local day, ready for the Insert* copies.
type Rows struct {
	Usage        []store.InsertUsageDailyParams
	ContextBands []store.InsertContextBandDailyParams
	Compactions  []store.InsertCompactionDailyParams
	Boundaries   []store.InsertCompactionEventsParams
	Sessions     []store.InsertSessionDailyParams
	Subagents    []store.InsertSubagentDailyParams
	SubagentRuns []store.InsertSubagentRunDailyParams
	Results      []store.InsertToolResultDailyParams
	ResultSizes  []store.InsertToolResultSizeDailyParams
	TopResults   []store.InsertToolResultTopParams
	Errors       []store.InsertToolErrorDailyParams
	// ItemsDropped counts events left out to stay in budget; the days they belong to are then incomplete.
	ItemsDropped int
	UniqueCalls  int
	// The scan figures of data.json's stats, for /api/status.
	Transcripts, DuplicateLines int
	Lines, Skipped              int64
	SkippedByClient             map[clients.ID]int64
	Unpriced                    int
	// Keys lists every shard replayed, and Contributors the shards with an event on each day.
	Keys         []string
	Contributors map[time.Time][]string

	counts  map[time.Time]*dayCount
	digests map[time.Time]string
}

// dayCount holds the run figures that belong to one day, so a day rebuilt alone keeps the totals exact.
type dayCount struct {
	duplicates, unique int
	unpriced           map[string]bool
}

// total sets the run figures from the per-day counts.
func (r *Rows) total() {
	r.DuplicateLines, r.UniqueCalls = 0, 0
	unpriced := map[string]bool{}
	for _, c := range r.counts {
		r.DuplicateLines += c.duplicates
		r.UniqueCalls += c.unique
		for m := range c.unpriced {
			unpriced[m] = true
		}
	}
	r.Unpriced = len(unpriced)
}

// UnpricedPair is a (client, model) with calls that have no price.
type UnpricedPair struct{ Client, Model string }

// UnpricedByDay lists the unpriced pairs of each day, with the model as the usage rows label it.
func (r *Rows) UnpricedByDay() map[time.Time][]UnpricedPair {
	out := map[time.Time][]UnpricedPair{}
	for day, c := range r.counts {
		for k := range c.unpriced {
			client, model, _ := strings.Cut(k, " ")
			out[day] = append(out[day], UnpricedPair{client, labels.Model(clients.ID(client), model)})
		}
	}
	return out
}

// UnpricedIn merges the pairs of the days from..to, one per pair, ordered by client then model; a nil client keeps all.
func UnpricedIn(byDay map[time.Time][]UnpricedPair, from, to time.Time, client *string) []UnpricedPair {
	seen := map[UnpricedPair]bool{}
	out := []UnpricedPair{}
	for day, pairs := range byDay {
		if day.Before(from) || day.After(to) {
			continue
		}
		for _, p := range pairs {
			if (client == nil || *client == p.Client) && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	slices.SortFunc(out, func(a, b UnpricedPair) int {
		return cmp.Or(strings.Compare(a.Client, b.Client), strings.Compare(a.Model, b.Model))
	})
	return out
}

type usageKey struct {
	day            time.Time
	client         store.ClientID
	project, model string
	scope          store.TokenScope
}

type bandKey struct {
	day     time.Time
	client  store.ClientID
	project string
	scope   store.TokenScope
	min     int64
}

type compactionKey struct {
	day     time.Time
	project string
	scope   store.TokenScope
	trigger store.CompactionTrigger
}

type sessionKey struct {
	day                     time.Time
	client                  store.ClientID
	project, session, model string
	scope                   store.TokenScope
}

type subKey struct {
	day                            time.Time
	project, session, agent, model string
}

type runKey struct {
	day                     time.Time
	project, session, agent string
}

type toolKey struct {
	day           time.Time
	project, tool string
	scope         store.TokenScope
}

type sizeKey struct {
	toolKey
	min int64
}

type errKey struct {
	toolKey
	class store.ErrorClass
}

func dayOf(sec int64) time.Time {
	if y, m, d := time.Unix(sec, 0).In(time.Local).Date(); y >= 0 && y <= 9999 {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	d, err := time.ParseInLocation("2006-01-02", LocalDay(sec), time.UTC)
	if err != nil {
		return time.Unix(sec, 0).UTC().Truncate(24 * time.Hour)
	}
	return d
}

func at(sec int64) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Unix(sec, 0).UTC(), Valid: true}
}

// BuildRows replays the shards as Aggregate does (the same dedupe, budget and segment rules) into the schema's tables.
func BuildRows(shards ShardSource, budget int, contextBands, resultBands []Band) (*Rows, error) {
	return buildRows(shards, budget, contextBands, resultBands, nil, nil)
}

// buildRows keeps the rows of the days emit accepts (every day when nil); the replay itself always covers every
// event, because dedupe, segments and runs carry across days. seen gets each visited shard with its days.
func buildRows(shards ShardSource, budget int, contextBands, resultBands []Band, emit func(time.Time) bool,
	seen func(*scan.Shard, []time.Time)) (*Rows, error) {
	if len(contextBands) == 0 {
		contextBands = DefaultContextBands
	}
	if len(resultBands) == 0 {
		resultBands = DefaultResultBands
	}
	first, sizes, err := mergedUsage(shards, unlimited, budget)
	if err != nil {
		return nil, err
	}
	perShard := ItemsPerShard(sizes, budget)
	best := first
	if perShard != unlimited {
		if best, _, err = mergedUsage(shards, perShard, budget); err != nil {
			return nil, err
		}
	}
	usage := map[usageKey]*store.InsertUsageDailyParams{}
	bands := map[bandKey]*store.InsertContextBandDailyParams{}
	compactions := map[compactionKey]*store.InsertCompactionDailyParams{}
	sessions := map[sessionKey]*store.InsertSessionDailyParams{}
	subs := map[subKey]*store.InsertSubagentDailyParams{}
	runs := map[runKey]*store.InsertSubagentRunDailyParams{}
	tools := map[toolKey]*store.InsertToolResultDailyParams{}
	toolSizes := map[sizeKey]*store.InsertToolResultSizeDailyParams{}
	errs := map[errKey]*store.InsertToolErrorDailyParams{}
	top := map[string]*store.InsertToolResultTopParams{}
	var boundaries []store.InsertCompactionEventsParams
	seenCalls, seenUses, seenBoundaries := map[string]bool{}, map[string]bool{}, map[string]bool{}
	toolNames := map[string]string{}
	out := &Rows{Contributors: map[time.Time][]string{}, counts: map[time.Time]*dayCount{}}
	keep := func(day time.Time) bool { return emit == nil || emit(day) }
	counted := func(day time.Time) *dayCount {
		c := out.counts[day]
		if c == nil {
			c = &dayCount{}
			out.counts[day] = c
		}
		return c
	}
	rowCount := 0
	grow := func() error {
		rowCount++
		if rowCount > maxTableRows {
			return ErrTooManyRows
		}
		return nil
	}

	err = shards(func(shard *scan.Shard) error {
		if !shard.Client.Valid() {
			return fmt.Errorf("%w: %q in %s", ErrClientRefused, shard.Client, shard.Key)
		}
		client := store.ClientID(shard.Client)
		if shard.Client != clients.ClaudeCode && shard.Scope != "main" {
			return fmt.Errorf("%w: %s shard %s has scope %q", ErrClientRefused, shard.Client, shard.Key, shard.Scope)
		}
		out.Transcripts++
		out.Lines += shard.Lines
		out.Skipped += shard.Skipped
		out.addSkipped(shard.Client, shard.Skipped)
		project := label(projectLabel, shard.Project, "other")
		session := label(sessionLabel, shard.Session, "unknown")
		scope := store.TokenScopeMain
		agent := ""
		if shard.Scope == "sub" {
			scope = store.TokenScopeSub
			agent = "unknown"
			if shard.AgentType != nil {
				agent = label(agentLabel, *shard.AgentType, "unknown")
			}
		}
		type open struct {
			tool   string
			tokens int64
		}
		var segment []open
		openIndex := map[string]int{}
		var tracked []*store.InsertToolResultTopParams
		inSegment := map[string]bool{}
		waitingAfter := -1
		var run *store.InsertSubagentRunDailyParams
		var runFirst, runPeak, runCalls int64
		runDay := time.Time{}
		kept := min(len(shard.Events), perShard)
		out.ItemsDropped += len(shard.Events) - kept
		var shardDays []time.Time
		for _, e := range shard.Events[:kept] {
			day := dayOf(e.Ts)
			if n := len(shardDays); n == 0 || !shardDays[n-1].Equal(day) && !slices.ContainsFunc(shardDays, day.Equal) {
				shardDays = append(shardDays, day)
			}
			on := keep(day)
			if e.Kind != extract.Call && shard.Client != clients.ClaudeCode {
				return fmt.Errorf("%w: %s shard %s holds a non-call event", ErrClientRefused, shard.Client, shard.Key)
			}
			switch e.Kind {
			case extract.Call:
				if seenCalls[e.ID] {
					if on {
						counted(day).duplicates++
					}
					continue
				}
				seenCalls[e.ID] = true
				call := mergedOf(e)
				if b, ok := best[e.ID]; ok {
					call = *b
				}
				u := call.N
				context := u[0] + u[1] + u[2] + u[3]
				if on {
					c := counted(day)
					c.unique++
					cost, priced, err := callCost(shard.Client, e.Text, call)
					if err != nil {
						return err
					}
					if !priced {
						if c.unpriced == nil {
							c.unpriced = map[string]bool{}
						}
						c.unpriced[string(shard.Client)+" "+e.Text] = true
					}
					if err := addCall(&rowMaps{usage, bands, sessions, subs}, grow, contextBands, e, day, client,
						project, session, agent, scope, u, cost); err != nil {
						return err
					}
				}
				if scope == store.TokenScopeSub {
					if run == nil {
						run = &store.InsertSubagentRunDailyParams{}
						runDay, runFirst, runPeak = day, context, context
					}
					runPeak = max(runPeak, context)
					runCalls++
				}
				if waitingAfter >= 0 {
					after := context
					boundaries[waitingAfter].TokensAfter = &after
					waitingAfter = -1
				}
				if len(segment) > 0 {
					if on {
						rate := 0.0
						if context > 0 {
							rate = pricing.CostOf(shard.Client, e.Text, pricing.Usage{CacheRead: context}).CacheRead / float64(context)
						}
						for _, o := range segment {
							tk := toolKey{day, project, o.tool, scope}
							tr := tools[tk]
							if tr == nil {
								if err := grow(); err != nil {
									return err
								}
								tr = &store.InsertToolResultDailyParams{Day: day, Project: project, Scope: scope,
									Tool: o.tool, RepeatMeasured: true}
								tools[tk] = tr
							}
							tr.WeightedTokens += o.tokens
							tr.WeightedCostUsd += float64(o.tokens) * rate
						}
					}
					for _, r := range tracked {
						r.Resends++
						r.WeightedTokens += r.Tokens
					}
				}
			case extract.Result:
				tool, ok := toolNames[e.Text]
				if !ok {
					tool = toolOf(e.Text)
					toolNames[e.Text] = tool
				}
				tokens := e.N[0]
				if !inSegment[e.ID] {
					inSegment[e.ID] = true
					held := tool
					if _, isOpen := openIndex[tool]; !isOpen && len(segment) >= maxSegmentLabels {
						held = "other"
					}
					i, isOpen := openIndex[held]
					if !isOpen {
						i = len(segment)
						openIndex[held] = i
						segment = append(segment, open{tool: held})
					}
					segment[i].tokens += tokens
				}
				if seenUses[e.ID] {
					continue
				}
				seenUses[e.ID] = true
				if on {
					tk := toolKey{day, project, tool, scope}
					tr := tools[tk]
					if tr == nil {
						if err := grow(); err != nil {
							return err
						}
						tr = &store.InsertToolResultDailyParams{Day: day, Project: project, Scope: scope, Tool: tool,
							RepeatMeasured: true}
						tools[tk] = tr
					}
					tr.Results++
					if e.Repeat {
						tr.RepeatResults++
					}
					tr.Tokens += tokens
					tr.MaxTokens = max(tr.MaxTokens, tokens)
					szk := sizeKey{tk, bandFor(resultBands, tokens)}
					sz := toolSizes[szk]
					if sz == nil {
						if err := grow(); err != nil {
							return err
						}
						sz = &store.InsertToolResultSizeDailyParams{Day: day, Project: project, Scope: scope, Tool: tool,
							MinTokens: szk.min, MaxTokens: bandMax(resultBands, szk.min)}
						toolSizes[szk] = sz
					}
					sz.Results++
					sz.Tokens += tokens
					if class, ok := errorClasses[e.Error]; ok {
						ek := errKey{tk, class}
						er := errs[ek]
						if er == nil {
							if err := grow(); err != nil {
								return err
							}
							er = &store.InsertToolErrorDailyParams{Day: day, Project: project, Scope: scope, Tool: tool,
								ErrorClass: class}
							errs[ek] = er
						}
						er.Errors++
					}
				}
				if isHash16(e.ID) && len(tracked) < maxTrackedResults {
					r := &store.InsertToolResultTopParams{Result: e.ID, Day: day, Project: project, Scope: scope,
						Tool: tool, Tokens: tokens}
					if on {
						top[e.ID] = r
					}
					tracked = append(tracked, r)
				}
			default:
				segment, tracked = segment[:0], tracked[:0]
				clear(openIndex)
				clear(inSegment)
				if seenBoundaries[e.ID] {
					continue
				}
				seenBoundaries[e.ID] = true
				trigger := store.CompactionTriggerAuto
				if e.Text == "manual" {
					trigger = store.CompactionTriggerManual
				}
				if on {
					ck := compactionKey{day, project, scope, trigger}
					cr := compactions[ck]
					if cr == nil {
						if err := grow(); err != nil {
							return err
						}
						cr = &store.InsertCompactionDailyParams{Day: day, Project: project, Scope: scope, Trigger: trigger}
						compactions[ck] = cr
					}
					cr.Compactions++
					cr.TokensBefore += e.N[0]
				}
				waitingAfter = -1
				if on && boundaryKey.MatchString(e.ID) {
					if err := grow(); err != nil {
						return err
					}
					boundaries = append(boundaries, store.InsertCompactionEventsParams{Boundary: e.ID, At: at(e.Ts),
						Day: day, Project: project, Session: session, Scope: scope, Trigger: trigger,
						TokensBefore: e.N[0]})
					waitingAfter = len(boundaries) - 1
				}
			}
		}
		if run != nil && keep(runDay) {
			rk := runKey{runDay, project, session, agent}
			r := runs[rk]
			if r == nil {
				if err := grow(); err != nil {
					return err
				}
				r = &store.InsertSubagentRunDailyParams{Day: runDay, Project: project, Session: session, AgentType: agent}
				runs[rk] = r
			}
			r.Runs++
			r.FirstContextTokens += runFirst
			r.PeakContextTokens += runPeak
			r.Calls += runCalls
		}
		out.Keys = append(out.Keys, shard.Key)
		for _, d := range shardDays {
			if keep(d) {
				out.Contributors[d] = append(out.Contributors[d], shard.Key)
			}
		}
		if seen != nil {
			seen(shard, shardDays)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.Usage = values(usage)
	out.ContextBands = values(bands)
	out.Compactions = values(compactions)
	out.Boundaries = boundaries
	out.Sessions = values(sessions)
	out.Subagents = values(subs)
	out.SubagentRuns = values(runs)
	out.Results = values(tools)
	out.ResultSizes = values(toolSizes)
	out.Errors = values(errs)
	out.TopResults = topPerDay(top)
	out.sort()
	out.total()
	return out, nil
}

type rowMaps struct {
	usage    map[usageKey]*store.InsertUsageDailyParams
	bands    map[bandKey]*store.InsertContextBandDailyParams
	sessions map[sessionKey]*store.InsertSessionDailyParams
	subs     map[subKey]*store.InsertSubagentDailyParams
}

// addCall adds one owned call to the usage, band, session and sub-agent rows of its day.
func addCall(m *rowMaps, grow func() error, contextBands []Band, e extract.Event, day time.Time,
	client store.ClientID, project, session, agent string, scope store.TokenScope, u [5]int64, cost pricing.Cost) error {
	input, cw5, cw1h, cacheRead, output := u[0], u[1], u[2], u[3], u[4]
	model := labels.Model(clients.ID(client), e.Text)
	context := input + cw5 + cw1h + cacheRead
	total := cost.Input + cost.CacheWrite + cost.CacheRead + cost.Output
	uk := usageKey{day, client, project, model, scope}
	ur := m.usage[uk]
	if ur == nil {
		if err := grow(); err != nil {
			return err
		}
		ur = &store.InsertUsageDailyParams{Day: day, Project: project, Scope: scope, Model: model, Client: client}
		m.usage[uk] = ur
	}
	ur.Calls++
	ur.InputTokens += input
	ur.CacheWriteTokens += cw5 + cw1h
	ur.CacheReadTokens += cacheRead
	ur.OutputTokens += output
	ur.ContextTokens += context
	ur.CostInputUsd += cost.Input
	ur.CostCacheWriteUsd += cost.CacheWrite
	ur.CostCacheReadUsd += cost.CacheRead
	ur.CostOutputUsd += cost.Output

	bk := bandKey{day, client, project, scope, bandFor(contextBands, context)}
	br := m.bands[bk]
	if br == nil {
		if err := grow(); err != nil {
			return err
		}
		br = &store.InsertContextBandDailyParams{Day: day, Project: project, Scope: scope, MinTokens: bk.min,
			MaxTokens: bandMax(contextBands, bk.min), Client: client}
		m.bands[bk] = br
	}
	br.Calls++
	br.ContextTokens += context
	br.CacheReadTokens += cacheRead

	sk := sessionKey{day, client, project, session, model, scope}
	sr := m.sessions[sk]
	if sr == nil {
		if err := grow(); err != nil {
			return err
		}
		sr = &store.InsertSessionDailyParams{Day: day, Project: project, Session: session, Scope: scope,
			Model: model, FirstAt: at(e.Ts), LastAt: at(e.Ts), Client: client}
		m.sessions[sk] = sr
	}
	sr.Calls++
	sr.InputTokens += input
	sr.CacheWriteTokens += cw5 + cw1h
	sr.CacheReadTokens += cacheRead
	sr.OutputTokens += output
	sr.ContextTokens += context
	sr.PeakContextTokens = max(sr.PeakContextTokens, context)
	sr.CostUsd += total
	if t := at(e.Ts); !t.Time.Before(sr.LastAt.Time) {
		sr.LastAt, sr.LastContextTokens = t, context
	} else if t.Time.Before(sr.FirstAt.Time) {
		sr.FirstAt = t
	}

	if scope == store.TokenScopeSub {
		k := subKey{day, project, session, agent, model}
		r := m.subs[k]
		if r == nil {
			if err := grow(); err != nil {
				return err
			}
			r = &store.InsertSubagentDailyParams{Day: day, Project: project, Session: session,
				AgentType: agent, Model: model}
			m.subs[k] = r
		}
		r.Calls++
		r.ContextTokens += context
		r.CacheReadTokens += cacheRead
		r.OutputTokens += output
		r.CostUsd += total
	}
	return nil
}

func values[K comparable, V any](m map[K]*V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	return out
}

func topPerDay(all map[string]*store.InsertToolResultTopParams) []store.InsertToolResultTopParams {
	byDay := map[time.Time][]store.InsertToolResultTopParams{}
	for _, r := range all {
		if r.WeightedTokens > 0 || r.Tokens > 0 {
			byDay[r.Day] = append(byDay[r.Day], *r)
		}
	}
	var out []store.InsertToolResultTopParams
	for _, rows := range byDay {
		sort.Slice(rows, func(a, b int) bool {
			if rows[a].WeightedTokens != rows[b].WeightedTokens {
				return rows[a].WeightedTokens > rows[b].WeightedTokens
			}
			if rows[a].Tokens != rows[b].Tokens {
				return rows[a].Tokens > rows[b].Tokens
			}
			return rows[a].Result < rows[b].Result
		})
		out = append(out, rows[:min(len(rows), TopResultsPerDay)]...)
	}
	return out
}

func key(parts ...any) string { return fmt.Sprint(parts...) }

func (r *Rows) sort() {
	sortBy(r.Usage, func(x store.InsertUsageDailyParams) string { return key(x.Day, x.Client, x.Project, x.Scope, x.Model) })
	sortBy(r.ContextBands, func(x store.InsertContextBandDailyParams) string {
		return key(x.Day, x.Client, x.Project, x.Scope, fmt.Sprintf("%020d", x.MinTokens))
	})
	sortBy(r.Compactions, func(x store.InsertCompactionDailyParams) string {
		return key(x.Day, x.Project, x.Scope, x.Trigger)
	})
	sortBy(r.Boundaries, func(x store.InsertCompactionEventsParams) string { return key(x.Day, x.Boundary) })
	sortBy(r.Sessions, func(x store.InsertSessionDailyParams) string {
		return key(x.Day, x.Client, x.Project, x.Session, x.Scope, x.Model)
	})
	sortBy(r.Subagents, func(x store.InsertSubagentDailyParams) string {
		return key(x.Day, x.Project, x.Session, x.AgentType, x.Model)
	})
	sortBy(r.SubagentRuns, func(x store.InsertSubagentRunDailyParams) string {
		return key(x.Day, x.Project, x.Session, x.AgentType)
	})
	sortBy(r.Results, func(x store.InsertToolResultDailyParams) string { return key(x.Day, x.Project, x.Scope, x.Tool) })
	sortBy(r.ResultSizes, func(x store.InsertToolResultSizeDailyParams) string {
		return key(x.Day, x.Project, x.Scope, x.Tool, fmt.Sprintf("%020d", x.MinTokens))
	})
	sortBy(r.TopResults, func(x store.InsertToolResultTopParams) string { return key(x.Day, x.Result) })
	sortBy(r.Errors, func(x store.InsertToolErrorDailyParams) string {
		return key(x.Day, x.Project, x.Scope, x.Tool, x.ErrorClass)
	})
}

func sortBy[T any](rows []T, k func(T) string) {
	keys := make(map[int]string, len(rows))
	idx := make([]int, len(rows))
	for i := range rows {
		idx[i] = i
		keys[i] = k(rows[i])
	}
	sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]] < keys[idx[b]] })
	sorted := make([]T, len(rows))
	for i, j := range idx {
		sorted[i] = rows[j]
	}
	copy(rows, sorted)
}

// Digests returns one hash per day over every row of that day, so a writer can tell which days changed. The rows
// must not change after the first call, which keeps the result.
func (r *Rows) Digests() map[time.Time]string {
	if r.digests == nil {
		r.digests = r.digest()
	}
	return maps.Clone(r.digests)
}

func (r *Rows) digest() map[time.Time]string {
	h := map[time.Time]*digest{}
	add := func(day time.Time, row any) {
		d := h[day]
		if d == nil {
			d = &digest{sum: sha256.New()}
			h[day] = d
		}
		fmt.Fprintf(d.sum, "%#v\n", row)
	}
	for _, x := range r.Usage {
		add(x.Day, x)
	}
	for _, x := range r.ContextBands {
		add(x.Day, flat(x.Day, x.Client, x.Project, x.Scope, x.MinTokens, x.MaxTokens, x.Calls, x.ContextTokens,
			x.CacheReadTokens))
	}
	for _, x := range r.Compactions {
		add(x.Day, x)
	}
	for _, x := range r.Boundaries {
		add(x.Day, flat(x.Boundary, x.At.Time.Unix(), x.Project, x.Session, x.Scope, x.Trigger, x.TokensBefore,
			x.TokensAfter))
	}
	for _, x := range r.Sessions {
		add(x.Day, flat(x.Client, x.Project, x.Session, x.Scope, x.Model, x.Calls, x.InputTokens, x.CacheWriteTokens,
			x.CacheReadTokens, x.OutputTokens, x.ContextTokens, x.PeakContextTokens, x.LastContextTokens, x.CostUsd,
			x.FirstAt.Time.Unix(), x.LastAt.Time.Unix()))
	}
	for _, x := range r.Subagents {
		add(x.Day, x)
	}
	for _, x := range r.SubagentRuns {
		add(x.Day, x)
	}
	for _, x := range r.Results {
		add(x.Day, x)
	}
	for _, x := range r.ResultSizes {
		add(x.Day, flat(x.Project, x.Scope, x.Tool, x.MinTokens, x.MaxTokens, x.Results, x.Tokens))
	}
	for _, x := range r.TopResults {
		add(x.Day, x)
	}
	for _, x := range r.Errors {
		add(x.Day, x)
	}
	out := make(map[time.Time]string, len(h))
	for day, d := range h {
		out[day] = hex.EncodeToString(d.sum.Sum(nil))[:32]
	}
	return out
}

type digest struct{ sum hash.Hash }

// flat prints pointers by value, so two equal rows hash alike.
func flat(parts ...any) []any {
	out := make([]any, len(parts))
	for i, p := range parts {
		if v, ok := p.(*int64); ok {
			if v == nil {
				out[i] = "nil"
			} else {
				out[i] = *v
			}
			continue
		}
		out[i] = p
	}
	return out
}

// Days lists every day that has a row, oldest first.
func (r *Rows) Days() []time.Time {
	var days []time.Time
	for d := range r.Digests() {
		days = append(days, d)
	}
	slices.SortFunc(days, func(a, b time.Time) int { return a.Compare(b) })
	return days
}

// Only keeps the rows of the given days.
func (r *Rows) Only(days map[time.Time]bool) *Rows {
	return &Rows{
		Usage: pick(r.Usage, days, func(x store.InsertUsageDailyParams) time.Time { return x.Day }),
		ContextBands: pick(r.ContextBands, days,
			func(x store.InsertContextBandDailyParams) time.Time { return x.Day }),
		Compactions: pick(r.Compactions, days, func(x store.InsertCompactionDailyParams) time.Time { return x.Day }),
		Boundaries:  pick(r.Boundaries, days, func(x store.InsertCompactionEventsParams) time.Time { return x.Day }),
		Sessions:    pick(r.Sessions, days, func(x store.InsertSessionDailyParams) time.Time { return x.Day }),
		Subagents:   pick(r.Subagents, days, func(x store.InsertSubagentDailyParams) time.Time { return x.Day }),
		SubagentRuns: pick(r.SubagentRuns, days,
			func(x store.InsertSubagentRunDailyParams) time.Time { return x.Day }),
		Results: pick(r.Results, days, func(x store.InsertToolResultDailyParams) time.Time { return x.Day }),
		ResultSizes: pick(r.ResultSizes, days,
			func(x store.InsertToolResultSizeDailyParams) time.Time { return x.Day }),
		TopResults: pick(r.TopResults, days, func(x store.InsertToolResultTopParams) time.Time { return x.Day }),
		Errors:     pick(r.Errors, days, func(x store.InsertToolErrorDailyParams) time.Time { return x.Day }),
	}
}

func pick[T any](rows []T, days map[time.Time]bool, day func(T) time.Time) []T {
	var out []T
	for _, r := range rows {
		if days[day(r)] {
			out = append(out, r)
		}
	}
	return out
}

func (r *Rows) addSkipped(client clients.ID, n int64) {
	if r.SkippedByClient == nil {
		r.SkippedByClient = map[clients.ID]int64{}
	}
	r.SkippedByClient[client] += n
}
