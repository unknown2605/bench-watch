package server

import (
	"context"
	"log"
	"math"
	"regexp"
	"sort"
	"sync"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/guard"
	"benchwatch/server/internal/store"
	"benchwatch/server/internal/transcripts"
)

func (s *Server) GetFills(ctx context.Context, r api.GetFillsRequestObject) (api.GetFillsResponseObject, error) {
	out, err := s.fills(ctx, r.Params)
	if err == nil {
		return api.GetFills200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetFills400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 422:
		return api.GetFills422JSONResponse{NotForClientJSONResponse: api.NotForClientJSONResponse{Code: code}}, nil
	case ok && st == 503:
		return api.GetFills503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

type fillGroup = struct {
	Project        *api.ProjectLabel `json:"project,omitempty"`
	Results        int64             `json:"results"`
	SizeBand       *api.TokenBand    `json:"sizeBand,omitempty"`
	Tokens         int64             `json:"tokens"`
	Tool           *api.ToolLabel    `json:"tool,omitempty"`
	WeightedTokens int64             `json:"weightedTokens"`
}

type toolWeight = struct {
	Tool           api.ToolLabel `json:"tool"`
	WeightedTokens int64         `json:"weightedTokens"`
}

type projectFills = struct {
	Project        api.ProjectLabel `json:"project"`
	Tools          []toolWeight     `json:"tools"`
	WeightedTokens int64            `json:"weightedTokens"`
}

// p95 is the upper bound of the size band holding the 95th percentile result; nil for the open top band.
func p95(bands []store.ToolSizeBandsRow) *int64 {
	sort.Slice(bands, func(i, j int) bool { return bands[i].MinTokens < bands[j].MinTokens })
	var all, seen int64
	for _, b := range bands {
		all += b.Results
	}
	for _, b := range bands {
		seen += b.Results
		if all > 0 && seen*100 >= all*95 {
			return b.MaxTokens
		}
	}
	return nil
}

func (s *Server) fills(ctx context.Context, prm api.GetFillsParams) (api.FillsResponse, error) {
	if err := s.refuseClient(prm.Range, prm.From, prm.To, prm.Client); err != nil {
		return api.FillsResponse{}, err
	}
	group := api.FillsGroupTool
	if prm.Group != nil {
		group = *prm.Group
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.FillsResponse{}, err
	}
	q := s.q()
	w := p.cur
	stats, err := q.ToolStats(ctx, store.ToolStatsParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return api.FillsResponse{}, err
	}
	out := api.FillsResponse{Period: p.Period, Group: group, Tools: []api.ToolStats{}, Groups: []fillGroup{},
		ByProject: []projectFills{}}
	if _, out.ResultSizeBands, err = s.tokenBands(ctx); err != nil {
		return out, err
	}
	sizes, err := q.ToolSizeBands(ctx, store.ToolSizeBandsParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return out, err
	}
	byTool := map[string][]store.ToolSizeBandsRow{}
	for _, b := range sizes {
		byTool[b.Tool] = append(byTool[b.Tool], b)
	}
	var results, weighted int64
	for _, t := range stats {
		results += t.Results
		weighted += t.WeightedTokens
		out.Tokens += t.Tokens
		tool := api.ToolStats{Tool: t.Tool, Results: t.Results, Tokens: t.Tokens, WeightedTokens: t.WeightedTokens,
			MaxTokens: t.MaxTokens, P95Tokens: p95(byTool[t.Tool]), CostUsd: t.WeightedCostUsd,
			RepeatMeasuredResults: t.RepeatMeasuredResults}
		if t.RepeatMeasuredResults > 0 {
			tool.RepeatResults = &t.RepeatResults
		}
		out.Tools = append(out.Tools, tool)
	}
	sort.SliceStable(out.Tools, func(i, j int) bool { return out.Tools[i].WeightedTokens > out.Tools[j].WeightedTokens })
	out.Results = api.Delta{Value: float64(results)}
	if p.prev != nil {
		prev, err := q.ToolStats(ctx, store.ToolStatsParams{DayFrom: p.prev.from, DayTo: p.prev.to,
			Project: w.project})
		if err != nil {
			return out, err
		}
		var n int64
		for _, t := range prev {
			n += t.Results
		}
		out.Results.Previous = ptr(float64(n))
	}
	cur, err := s.usage(ctx, w)
	if err != nil {
		return out, err
	}
	out.ContextShare = share(float64(weighted), float64(cur.ContextTokens))
	largest, err := q.ToolLargestResult(ctx, store.ToolLargestResultParams{DayFrom: w.from, DayTo: w.to,
		Project: w.project})
	switch {
	case err == nil:
		out.Largest = &struct {
			Day     openapi_types.Date `json:"day"`
			Project api.ProjectLabel   `json:"project"`
			Tokens  int64              `json:"tokens"`
			Tool    api.ToolLabel      `json:"tool"`
		}{date(largest.Day), largest.Project, largest.MaxTokens, largest.Tool}
	case !notFound(err):
		return out, err
	}
	projects, err := q.ToolByProject(ctx, store.ToolByProjectParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return out, err
	}
	out.ByProject = fillsByProject(projects)
	switch group {
	case api.FillsGroupTool:
		for _, t := range out.Tools {
			out.Groups = append(out.Groups, fillGroup{Tool: ptr(t.Tool), Results: t.Results, Tokens: t.Tokens,
				WeightedTokens: t.WeightedTokens})
		}
	case api.FillsGroupProject:
		sums := map[string]*fillGroup{}
		for _, r := range projects {
			g := sums[r.Project]
			if g == nil {
				g = &fillGroup{Project: ptr(r.Project)}
				sums[r.Project] = g
			}
			g.Results += r.Results
			g.Tokens += r.Tokens
			g.WeightedTokens += r.WeightedTokens
		}
		for _, g := range sums {
			out.Groups = append(out.Groups, *g)
		}
		sort.Slice(out.Groups, func(i, j int) bool {
			a, b := out.Groups[i], out.Groups[j]
			return a.WeightedTokens > b.WeightedTokens || a.WeightedTokens == b.WeightedTokens && *a.Project < *b.Project
		})
	case api.FillsGroupSizeBand:
		out.Groups = sizeBandGroups(sizes, stats)
	}
	top, err := q.TopToolResults(ctx, store.TopToolResultsParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		MaxRows: maxTopResults})
	if err != nil {
		return out, err
	}
	out.Top = make([]struct {
		Day            openapi_types.Date `json:"day"`
		Project        api.ProjectLabel   `json:"project"`
		Resends        int32              `json:"resends"`
		Result         api.Hash16         `json:"result"`
		Tokens         int64              `json:"tokens"`
		Tool           api.ToolLabel      `json:"tool"`
		WeightedTokens int64              `json:"weightedTokens"`
	}, len(top))
	for i, t := range top {
		o := &out.Top[i]
		o.Day, o.Project, o.Resends, o.Result, o.Tokens, o.Tool, o.WeightedTokens = date(t.Day), t.Project, t.Resends,
			t.Result, t.Tokens, t.Tool, t.WeightedTokens
	}
	recent := lastDays(w, recentDays)
	daily, err := q.ToolDaily(ctx, store.ToolDailyParams{DayFrom: recent.from, DayTo: recent.to, Project: w.project})
	if err != nil {
		return out, err
	}
	out.Daily = make([]struct {
		Day     openapi_types.Date `json:"day"`
		Results int64              `json:"results"`
		Tokens  int64              `json:"tokens"`
		Tool    api.ToolLabel      `json:"tool"`
	}, len(daily))
	for i, d := range daily {
		out.Daily[i].Day, out.Daily[i].Results, out.Daily[i].Tokens, out.Daily[i].Tool = date(d.Day), d.Results,
			d.Tokens, d.Tool
	}
	return out, nil
}

// sizeBandGroups apportions each tool's weighted tokens over its bands by token share, because the size table keeps
// no re-send counts; exact when a tool's results are re-sent alike.
func sizeBandGroups(sizes []store.ToolSizeBandsRow, stats []store.ToolStatsRow) []fillGroup {
	toolTokens := map[string]store.ToolStatsRow{}
	for _, t := range stats {
		toolTokens[t.Tool] = t
	}
	sums := map[int64]*fillGroup{}
	weighted := map[int64]float64{}
	for _, b := range sizes {
		g := sums[b.MinTokens]
		if g == nil {
			g = &fillGroup{SizeBand: &api.TokenBand{MinTokens: b.MinTokens, MaxTokens: b.MaxTokens}}
			sums[b.MinTokens] = g
		}
		g.Results += b.Results
		g.Tokens += b.Tokens
		if t := toolTokens[b.Tool]; t.Tokens > 0 {
			weighted[b.MinTokens] += float64(t.WeightedTokens) * float64(b.Tokens) / float64(t.Tokens)
		}
	}
	out := make([]fillGroup, 0, len(sums))
	for lo, g := range sums {
		g.WeightedTokens = int64(math.Round(weighted[lo]))
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SizeBand.MinTokens < out[j].SizeBand.MinTokens })
	return out
}

func fillsByProject(rows []store.ToolByProjectRow) []projectFills {
	byProject := map[string]*projectFills{}
	for _, r := range rows {
		p := byProject[r.Project]
		if p == nil {
			p = &projectFills{Project: r.Project, Tools: []toolWeight{}}
			byProject[r.Project] = p
		}
		p.WeightedTokens += r.WeightedTokens
		p.Tools = append(p.Tools, toolWeight{Tool: r.Tool, WeightedTokens: r.WeightedTokens})
	}
	out := make([]projectFills, 0, len(byProject))
	for _, p := range byProject {
		sort.Slice(p.Tools, func(i, j int) bool {
			a, b := p.Tools[i], p.Tools[j]
			return a.WeightedTokens > b.WeightedTokens || a.WeightedTokens == b.WeightedTokens && a.Tool < b.Tool
		})
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].WeightedTokens > out[j].WeightedTokens ||
			out[i].WeightedTokens == out[j].WeightedTokens && out[i].Project < out[j].Project
	})
	return out
}

func (s *Server) GetGuard(ctx context.Context, r api.GetGuardRequestObject) (api.GetGuardResponseObject, error) {
	out, err := s.guard(ctx, r.Params)
	if err == nil {
		return api.GetGuard200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetGuard400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 422:
		return api.GetGuard422JSONResponse{NotForClientJSONResponse: api.NotForClientJSONResponse{Code: code}}, nil
	case ok && st == 503:
		return api.GetGuard503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

// alloc points *p at a new zero value, for the generated response's anonymous struct types.
func alloc[T any](p **T) *T {
	*p = new(T)
	return *p
}

// sized never returns nil: the spec's arrays are not nullable, and a nil slice encodes as null.
func sized[T any](_ []T, n int) []T { return make([]T, n) }

var guardErrorCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

// guardCode keeps a collector code that fits the API's ErrorCode pattern.
func guardCode(code string) string {
	if guardErrorCode.MatchString(code) {
		return code
	}
	return "EUNKNOWN"
}

// warnedGuardStates holds the unknown collector states already logged.
var warnedGuardStates sync.Map

// guardLogState names the collector's state for the API; code is set only for unreadable. Stored figures are served
// whatever the state, so only an unavailable log needs the store to tell missing from absent.
func (s *Server) guardLogState(ctx context.Context, q *store.Queries) (api.GuardResponseLogState, *string, error) {
	if s.Guard == nil {
		return api.GuardResponseLogStateDisabled, nil, nil
	}
	return guardStateName(ctx, q, s.Guard.Status())
}

func guardStateName(ctx context.Context, q *store.Queries, st transcripts.GuardStatus) (api.GuardResponseLogState, *string, error) {
	switch st.State {
	case transcripts.GuardOK:
		return api.GuardResponseLogStateOk, nil, nil
	case transcripts.GuardStarting:
		return api.GuardResponseLogStateStarting, nil, nil
	case transcripts.GuardFailed:
		return api.GuardResponseLogStateUnreadable, ptr(guardCode(st.ErrorCode)), nil
	case transcripts.GuardDisabled:
		return api.GuardResponseLogStateDisabled, nil, nil
	case transcripts.GuardUnavailable:
		stored, err := q.GuardFiguresStored(ctx)
		if err != nil {
			return "", nil, err
		}
		if stored {
			return api.GuardResponseLogStateMissing, nil, nil
		}
		return api.GuardResponseLogStateAbsent, nil, nil
	}
	if _, seen := warnedGuardStates.LoadOrStore(st.State, true); !seen {
		log.Printf("WARN guard: unknown collector state %q", st.State)
	}
	return api.GuardResponseLogStateUnreadable, ptr("EUNKNOWN"), nil
}

func (s *Server) guard(ctx context.Context, prm api.GetGuardParams) (api.GuardResponse, error) {
	if err := s.refuseClient(prm.Range, prm.From, prm.To, prm.Client); err != nil {
		return api.GuardResponse{}, err
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.GuardResponse{}, err
	}
	q := s.q()
	w := p.cur
	span := func(w window) store.GuardCallTotalsParams {
		return store.GuardCallTotalsParams{DayFrom: w.from, DayTo: w.to}
	}
	t, err := q.GuardCallTotals(ctx, span(w))
	if err != nil {
		return api.GuardResponse{}, err
	}
	out := api.GuardResponse{Period: p.Period, Calls: api.Delta{Value: float64(t.Calls)}}
	if out.Log.State, out.Log.Code, err = s.guardLogState(ctx, q); err != nil {
		return out, err
	}
	if p.prev != nil {
		pt, err := q.GuardCallTotals(ctx, span(*p.prev))
		if err != nil {
			return out, err
		}
		out.Calls.Previous = ptr(float64(pt.Calls))
	}
	if t.Rows > 0 {
		d := alloc(&out.Decisions)
		d.Allow, d.Ask, d.Deny, d.FailClosed, d.TimeCap, d.SubDeny = t.Allow, t.Ask, t.Deny, t.FailClosed, t.TimeCaps,
			t.SubDeny
		sa := alloc(&out.SubAgent)
		sa.Calls, sa.Sub = t.Calls, t.SubCalls
	}
	if err := s.guardLogRow(ctx, q, &out); err != nil {
		return out, err
	}
	tools, err := q.GuardCallsByTool(ctx, store.GuardCallsByToolParams(span(w)))
	if err != nil {
		return out, err
	}
	lat, err := q.GuardCallLatencyPercentiles(ctx, store.GuardCallLatencyPercentilesParams(span(w)))
	if err != nil {
		return out, err
	}
	out.ByTool = sized(out.ByTool, len(tools))
	for i, r := range tools {
		o := &out.ByTool[i]
		o.Tool, o.Calls, o.Ask, o.Deny = api.GuardTool(r.Tool), r.Calls, r.Ask, r.Deny
		for _, l := range lat {
			if l.Tool != r.Tool {
				continue
			}
			switch math.Round(l.Quantile * 100) {
			case 50:
				o.P50Ms = ptr(l.Ms)
			case 95:
				o.P95Ms = ptr(l.Ms)
			case 99:
				o.P99Ms = ptr(l.Ms)
			}
		}
	}
	days, err := q.GuardCallsByDay(ctx, store.GuardCallsByDayParams(span(w)))
	if err != nil {
		return out, err
	}
	out.CallsByDay = sized(out.CallsByDay, len(days))
	for i, d := range days {
		o := &out.CallsByDay[i]
		o.Day, o.Calls, o.Ask, o.Deny = date(d.Day), d.Calls, d.Ask, d.Deny
	}
	hours, err := q.GuardCallHours(ctx, dayOf(s.now()))
	if err != nil {
		return out, err
	}
	if len(hours) > 0 || out.Log.State == api.GuardResponseLogStateOk {
		h := alloc(&out.Hours)
		*h = sized(*h, 24)
		for i := range *h {
			(*h)[i].Hour = int32(i)
		}
		for _, r := range hours {
			(*h)[r.Hour].Calls, (*h)[r.Hour].Deny = r.Calls, r.Deny
		}
	}
	rules, err := q.GuardTopRules(ctx, store.GuardTopRulesParams(span(w)))
	if err != nil {
		return out, err
	}
	out.Rules = sized(out.Rules, len(rules))
	for i, r := range rules {
		o := &out.Rules[i]
		o.Rule, o.Tool, o.Decision, o.Calls = r.Rule, api.GuardTool(r.Tool), api.GuardResponseRulesDecision(r.Decision),
			r.Calls
	}
	errs, err := q.ToolErrors(ctx, store.ToolErrorsParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return out, err
	}
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Errors > errs[j].Errors })
	s.setInstall(&out)
	trend, err := q.GuardBlockedTrend(ctx, store.GuardBlockedTrendParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return out, err
	}
	out.Trend = sized(out.Trend, len(trend))
	for i, d := range trend {
		out.Trend[i].Blocked, out.Trend[i].Day, out.Trend[i].Results = d.Blocked, date(d.Day), d.Results
	}
	out.ToolErrors = sized(out.ToolErrors, len(errs))
	for i, e := range errs {
		o := &out.ToolErrors[i]
		o.ErrorClass, o.Errors, o.Results, o.Tool = api.ErrorClass(e.ErrorClass), e.Errors, e.Results, e.Tool
	}
	return out, nil
}

// guardLogRow sets the reader's skip counters, last read and last deny; all stay empty before the first pass.
func (s *Server) guardLogRow(ctx context.Context, q *store.Queries, out *api.GuardResponse) error {
	st, err := q.GuardLogState(ctx)
	if notFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	k := &out.Log.Skipped
	k.Malformed, k.Oversized, k.BadVersion, k.BadRule = st.Malformed, st.Oversized, st.BadVersion, st.BadRule
	// The stored count is the last pass's snapshot; only a state that ran a pass over files vouches for it.
	if out.Log.State == api.GuardResponseLogStateOk || out.Log.State == api.GuardResponseLogStateUnreadable {
		k.NotRegular = st.NotRegular
	}
	if st.ReadAt.Valid {
		out.Log.LastReadAt = ptr(st.ReadAt.Time.UTC())
	}
	if st.LastDenyAt.Valid && st.LastDenyTool != nil {
		b := alloc(&out.LastBlock)
		b.At, b.Tool, b.Rule = st.LastDenyAt.Time.UTC(), api.GuardTool(*st.LastDenyTool), st.LastDenyRule
	}
	return nil
}

func (s *Server) setInstall(out *api.GuardResponse) {
	in := guard.Install{Source: "none", Bash: "unknown", File: "unknown"}
	if s.GuardInstall != nil {
		in = s.GuardInstall.Get()
	}
	i := &out.Install
	i.Source, i.Bash, i.File = api.GuardResponseInstallSource(in.Source), api.GuardMode(in.Bash), api.GuardMode(in.File)
	if !in.InstalledAt.IsZero() {
		i.InstalledAt = ptr(in.InstalledAt)
	}
	if in.Build != "" {
		i.Build = ptr(in.Build)
	}
}
