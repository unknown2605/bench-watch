// Package aggregate turns shards into the day-keyed tables of data.json, row for row
// as the retired Node scanner (4ab56da, lib/aggregate.mjs) did.
package aggregate

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"time"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/pricing"
	"benchwatch/server/internal/scan"
)

const (
	OutputVersion = 3
	// Every call re-adds each open label, so this keeps replay linear. The largest real segment holds 43.
	maxSegmentLabels = 256
	// Real data holds about 200k events; V8 throws past 2^24 entries in a Set or Map.
	MaxRunItems    = 4_000_000
	MaxOutputChars = 256 * 1024 * 1024
	// The largest real table holds about 7000 rows. Each row costs far more memory than the event behind it.
	maxTableRows = 1_000_000
	unlimited    = math.MaxInt
)

var (
	Bands  = []string{"<100k", "100-200k", "200-400k", "400-700k", ">700k"}
	Scopes = []string{"main", "sub"}
	// ErrTooManyRows is the RangeError that makes aggregateToFit retry with a smaller budget.
	ErrTooManyRows = errors.New("too many rows")
	// ErrClientRefused is a shard of an unknown client, or a non-Claude shard with sub-agent scope or non-call
	// events, which only Claude Code transcripts have.
	ErrClientRefused = errors.New("client shard refused")
)

func BandOf(context int64) int {
	switch {
	case context < 1e5:
		return 0
	case context < 2e5:
		return 1
	case context < 4e5:
		return 2
	case context < 7e5:
		return 3
	}
	return 4
}

func LocalDay(sec int64) string {
	return time.Unix(sec, 0).In(time.Local).Format("2006-01-02")
}

type dim struct {
	index  map[string]int
	values []string
}

func newDim() *dim { return &dim{index: map[string]int{}} }

func (d *dim) id(value string) int {
	if i, ok := d.index[value]; ok {
		return i
	}
	d.index[value] = len(d.values)
	d.values = append(d.values, value)
	return len(d.values) - 1
}

type rowKey struct {
	n    int
	keys [5]int
}

// table keeps rows in first-insertion order, as a JavaScript Map does.
type table struct {
	index map[rowKey]int
	rows  [][]float64
}

func newTable() *table { return &table{index: map[rowKey]int{}} }

func (t *table) add(keys []int, values ...float64) ([]float64, bool, error) {
	k := rowKey{n: len(keys)}
	copy(k.keys[:], keys)
	if i, ok := t.index[k]; ok {
		row := t.rows[i]
		for j, v := range values {
			row[len(keys)+j] += v
		}
		return row, true, nil
	}
	if len(t.rows) >= maxTableRows {
		return nil, false, ErrTooManyRows
	}
	row := make([]float64, 0, len(keys)+len(values))
	for _, key := range keys {
		row = append(row, float64(key))
	}
	row = append(row, values...)
	t.index[k] = len(t.rows)
	t.rows = append(t.rows, row)
	return row, false, nil
}

// list rounds from column digitsFrom on as Math.round(x * 1e6) / 1e6; float64() keeps the product unfused.
func (t *table) list(digitsFrom int) [][]float64 {
	if digitsFrom >= 0 {
		for _, r := range t.rows {
			for i := digitsFrom; i < len(r); i++ {
				r[i] = extract.RoundJS(float64(r[i]*1e6)) / 1e6
			}
		}
	}
	return t.rows
}

// ItemsPerShard is the most events each shard may keep so that the sum fits: the largest give way first, so healthy
// files always land.
func ItemsPerShard(sizes []int, budget int) int {
	sorted := append([]int(nil), sizes...)
	sort.Ints(sorted)
	left := budget
	for i, size := range sorted {
		share := left / (len(sorted) - i)
		if size > share {
			return share
		}
		left -= size
	}
	return unlimited
}

// ShardSource visits every shard in ownership order; each call must start a fresh pass.
type ShardSource func(visit func(*scan.Shard) error) error

// merged is a call's largest figures over every copy of its ID: each count and the recorded USD apart.
type merged struct {
	N      [5]int64
	USD    float64
	HasUSD bool
}

func mergedOf(e extract.Event) merged { return merged{N: e.N, USD: e.USD, HasUSD: e.HasUSD} }

func mergedUsage(shards ShardSource, perShard, budget int) (map[string]*merged, []int, error) {
	best := map[string]*merged{}
	var sizes []int
	err := shards(func(s *scan.Shard) error {
		sizes = append(sizes, len(s.Events))
		for _, e := range s.Events[:min(len(s.Events), perShard)] {
			if e.Kind != extract.Call {
				continue
			}
			seen, ok := best[e.ID]
			if !ok {
				if len(best) < budget {
					m := mergedOf(e)
					best[e.ID] = &m
				}
				continue
			}
			for i := range seen.N {
				seen.N[i] = max(seen.N[i], e.N[i])
			}
			if e.HasUSD {
				seen.USD, seen.HasUSD = max(seen.USD, e.USD), true
			}
		}
		return nil
	})
	return best, sizes, err
}

// callCost prices one call; priced is false when the cost is unknown, which callers count per (client, model).
// A Recorded client takes the USD it stored; one that stored none stays unpriced instead of costing $0.
func callCost(client clients.ID, model string, m merged) (cost pricing.Cost, priced bool, err error) {
	u := m.N
	p := pricing.PriceOf(client, model)
	if p.Basis == pricing.Recorded {
		if !m.HasUSD {
			return pricing.Cost{Basis: pricing.Recorded}, false, nil
		}
		cost, err = pricing.RecordedCost(client, m.USD)
		return cost, err == nil, err
	}
	cost = pricing.CostOf(client, model, pricing.Usage{Input: u[0], CW5: u[1], CW1h: u[2], CacheRead: u[3], Output: u[4]})
	return cost, cost.Basis == pricing.Listed, nil
}

type Stats struct {
	Transcripts           int   `json:"transcripts"`
	Lines                 int64 `json:"lines"`
	Skipped               int64 `json:"skipped"`
	UniqueCalls           int   `json:"uniqueCalls"`
	DuplicateLinesDropped int   `json:"duplicateLinesDropped"`
	ItemsDropped          int   `json:"itemsDropped"`
	Unreadable            *int  `json:"unreadable,omitempty"`
	Unrebuilt             *int  `json:"unrebuilt,omitempty"`
}

type Data struct {
	Version      int                `json:"version"`
	Bands        []string           `json:"bands"`
	Scopes       []string           `json:"scopes"`
	Days         []string           `json:"days"`
	Projects     []string           `json:"projects"`
	Models       []string           `json:"models"`
	Labels       []string           `json:"labels"`
	AgentTypes   []string           `json:"agentTypes"`
	ErrorClasses []string           `json:"errorClasses"`
	Sessions     [][2]any           `json:"sessions"`
	Usage        [][]float64        `json:"usage"`
	BandRows     [][]float64        `json:"bandRows"`
	Compactions  [][]float64        `json:"compactions"`
	SessionRows  [][]float64        `json:"sessionRows"`
	AgentRows    [][]float64        `json:"agentRows"`
	AgentRuns    [][]float64        `json:"agentRuns"`
	Fills        [][]float64        `json:"fills"`
	Errors       [][]float64        `json:"errors"`
	Prices       []pricing.PriceRow `json:"prices"`
	// Unpriced holds one model per unpriced (client, model) pair; UnpricedClients[i] is the client of Unpriced[i].
	Unpriced        []string `json:"unpriced"`
	UnpricedClients []string `json:"unpricedClients"`
	Stats           Stats    `json:"stats"`
	Guard           any      `json:"guard,omitempty"`
	GeneratedAt     string   `json:"generatedAt,omitempty"`
	Scan            any      `json:"scan,omitempty"`
}

type run struct {
	day         int
	first, peak int64
	calls       int64
	started     bool
}

// openLabels is a Map of label id to tokens, iterated in first-insertion order.
type openLabels struct {
	order  []int
	tokens map[int]int64
}

func (o *openLabels) clear() {
	o.order = o.order[:0]
	clear(o.tokens)
}

func Aggregate(shards ShardSource, budget int) (*Data, error) {
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
	days, projects, models, labelDim, agents, sessions, errorDim := newDim(), newDim(), newDim(), newDim(), newDim(), newDim(), newDim()
	usage, bands, compactions, sessionRows, agentRows, agentRuns, fills, errorRows :=
		newTable(), newTable(), newTable(), newTable(), newTable(), newTable(), newTable(), newTable()
	seenCalls, seenUses, seenBoundaries := map[string]bool{}, map[string]bool{}, map[string]bool{}
	sessionProject := map[int][2]any{}
	unpriced := newDim()
	var unpricedModels, unpricedClients []string
	var stats Stats

	err = shards(func(shard *scan.Shard) error {
		stats.Transcripts++
		stats.Lines += shard.Lines
		stats.Skipped += shard.Skipped
		p := projects.id(shard.Project)
		scope := 0
		if shard.Scope == "sub" {
			scope = 1
		}
		sessionID := sessions.id(strconv.Itoa(p) + ":" + shard.Session)
		sessionProject[sessionID] = [2]any{shard.Session, p}
		agent := -1
		if shard.Scope == "sub" {
			agentType := "unknown"
			if shard.AgentType != nil {
				agentType = *shard.AgentType
			}
			agent = agents.id(agentType)
		}
		open := openLabels{tokens: map[int]int64{}}
		inSegment := map[string]bool{}
		var r run
		kept := min(len(shard.Events), perShard)
		stats.ItemsDropped += len(shard.Events) - kept
		for _, e := range shard.Events[:kept] {
			day := days.id(LocalDay(e.Ts))
			switch e.Kind {
			case extract.Call:
				if seenCalls[e.ID] {
					stats.DuplicateLinesDropped++
					continue
				}
				seenCalls[e.ID] = true
				call := mergedOf(e)
				if b, ok := best[e.ID]; ok {
					call = *b
				}
				u := call.N
				input, cw5, cw1h, cacheRead, output := u[0], u[1], u[2], u[3], u[4]
				m := models.id(e.Text)
				context := input + cw5 + cw1h + cacheRead
				cost, priced, err := callCost(shard.Client, e.Text, call)
				if err != nil {
					return err
				}
				if !priced {
					if n := len(unpriced.values); unpriced.id(string(shard.Client)+" "+e.Text) == n {
						unpricedModels, unpricedClients = append(unpricedModels, e.Text),
							append(unpricedClients, string(shard.Client))
					}
				}
				totalCost := cost.Input + cost.CacheWrite + cost.CacheRead + cost.Output
				if _, _, err := usage.add([]int{day, p, scope, m}, 1, float64(input), float64(cw5+cw1h), float64(cacheRead),
					float64(output), float64(context), cost.Input, cost.CacheWrite, cost.CacheRead, cost.Output); err != nil {
					return err
				}
				if _, _, err := bands.add([]int{day, p, scope, BandOf(context)}, 1, float64(cacheRead)); err != nil {
					return err
				}
				row, existed, err := sessionRows.add([]int{day, sessionID, scope, m}, 1, float64(context), totalCost, 0, 0, 0)
				if err != nil {
					return err
				}
				ts := float64(e.Ts)
				if existed {
					row[7] = math.Min(row[7], ts)
				} else {
					row[7] = ts
				}
				row[8] = math.Max(row[8], ts)
				row[9] = math.Max(row[9], float64(context))
				if agent >= 0 {
					if _, _, err := agentRows.add([]int{day, p, agent}, 1, float64(context), float64(cacheRead), totalCost); err != nil {
						return err
					}
					if !r.started {
						r = run{day: day, first: context, peak: context, started: true}
					}
					r.peak = max(r.peak, context)
					r.calls++
				}
				for _, label := range open.order {
					if _, _, err := fills.add([]int{day, p, scope, label}, 0, 0, float64(open.tokens[label])); err != nil {
						return err
					}
				}
			case extract.Result:
				label := labelDim.id(e.Text)
				tokens := e.N[0]
				if !inSegment[e.ID] {
					inSegment[e.ID] = true
					held := label
					if _, isOpen := open.tokens[label]; !isOpen && len(open.order) >= maxSegmentLabels {
						held = labelDim.id("other")
					}
					if _, isOpen := open.tokens[held]; !isOpen {
						open.order = append(open.order, held)
					}
					open.tokens[held] += tokens
				}
				if seenUses[e.ID] {
					continue
				}
				seenUses[e.ID] = true
				if _, _, err := fills.add([]int{day, p, scope, label}, 1, float64(tokens), 0); err != nil {
					return err
				}
				if e.Error != "" {
					if _, _, err := errorRows.add([]int{day, p, scope, label, errorDim.id(e.Error)}, 1); err != nil {
						return err
					}
				}
			default:
				open.clear()
				clear(inSegment)
				if seenBoundaries[e.ID] {
					continue
				}
				seenBoundaries[e.ID] = true
				trigger := 0
				if e.Text == "manual" {
					trigger = 1
				}
				if _, _, err := compactions.add([]int{day, p, scope, trigger}, 1, float64(e.N[0])); err != nil {
					return err
				}
			}
		}
		if r.started {
			if _, _, err := agentRuns.add([]int{r.day, p, agent}, 1, float64(r.first), float64(r.peak), float64(r.calls)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	order := make([]int, len(days.values))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return days.values[order[a]] < days.values[order[b]] })
	remap := make([]float64, len(order))
	sortedDays := make([]string, len(order))
	for fresh, old := range order {
		remap[old] = float64(fresh)
		sortedDays[fresh] = days.values[old]
	}
	reday := func(rows [][]float64) [][]float64 {
		out := make([][]float64, len(rows))
		for i, r := range rows {
			c := append([]float64(nil), r...)
			c[0] = remap[int(r[0])]
			out[i] = c
		}
		return out
	}
	sessionList := make([][2]any, len(sessions.values))
	for i := range sessionList {
		sessionList[i] = sessionProject[i]
	}
	stats.UniqueCalls = len(seenCalls)
	return &Data{
		Version: OutputVersion, Bands: Bands, Scopes: Scopes, Days: sortedDays,
		Projects: nonNil(projects.values), Models: nonNil(models.values), Labels: nonNil(labelDim.values),
		AgentTypes: nonNil(agents.values), ErrorClasses: nonNil(errorDim.values), Sessions: sessionList,
		Usage: reday(usage.list(10)), BandRows: reday(bands.list(-1)), Compactions: reday(compactions.list(-1)),
		SessionRows: reday(sessionRows.list(6)), AgentRows: reday(agentRows.list(6)), AgentRuns: reday(agentRuns.list(-1)),
		Fills: reday(fills.list(-1)), Errors: reday(errorRows.list(-1)),
		Prices: pricing.PriceTable(), Unpriced: nonNil(unpricedModels), UnpricedClients: nonNil(unpricedClients),
		Stats: stats,
	}, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// AggregateToFit retries with a quarter of the budget while the tables or the output outgrow their limits; size
// returns the output's length in UTF-16 units, as JSON.stringify(data).length counts it.
func AggregateToFit(shards ShardSource, budget, maxChars int, size func(*Data) (int, error)) (*Data, error) {
	for items := budget; ; items /= 4 {
		data, err := Aggregate(shards, items)
		if err == nil {
			n, err := size(data)
			if err != nil {
				return nil, err
			}
			if items == 0 || n <= maxChars {
				return data, nil
			}
			continue
		}
		if items == 0 || !errors.Is(err, ErrTooManyRows) {
			return nil, err
		}
	}
}
