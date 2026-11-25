package server

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/pricing"
	"benchwatch/server/internal/store"
)

// allRows stands in for "no limit" on queries that take one; a period holds far fewer rows.
const allRows = 1 << 30

const (
	maxSessions    = 500
	maxCompactions = 20
	maxTopDays     = 10
	maxTopResults  = 20
	recentDays     = 14
)

type window struct {
	from, to time.Time
	project  *string
	client   *string
}

type period struct {
	api.Period
	cur  window
	prev *window
}

// resolve validates the period before touching the database, so a bad request is a 400 even when it is down.
func (s *Server) resolve(ctx context.Context, rng *api.TranscriptRange, from, to *openapi_types.Date,
	project *string, client *api.Client) (period, error) {
	today := s.now()
	if _, err := api.ResolvePeriod(rng, from, to, today, nil); err != nil {
		return period{}, err
	}
	if s.DB == nil {
		return period{}, errNoDB
	}
	first, _, err := s.dayRange(ctx)
	if err != nil {
		return period{}, err
	}
	p, err := api.ResolvePeriod(rng, from, to, today, first)
	if err != nil {
		return period{}, err
	}
	out := period{Period: p, cur: window{p.From.Time, p.To.Time, project, clientParam(client)}}
	// A previous period wholly before the first day has no data, so its figures are null rather than 0.
	if p.PreviousFrom != nil && first != nil && !p.PreviousTo.Time.Before(dayOf(*first)) {
		out.prev = &window{p.PreviousFrom.Time, p.PreviousTo.Time, project, clientParam(client)}
	}
	return out, nil
}

// claude says whether the window's Claude-only figures apply: no client filter, or claude-code.
func (w window) claude() bool { return w.client == nil || *w.client == string(clients.ClaudeCode) }

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// failure maps an error to the status and code the spec allows; ok is false for a 500.
func failure(err error) (status int, code api.ErrorResponseCode, ok bool) {
	var pe *api.PeriodError
	switch {
	case errors.As(err, &pe):
		return 400, pe.Code, true
	case errors.Is(err, errNotForClient):
		return 422, api.ErrorResponseCodeNotForClient, true
	case notFound(err):
		return 404, api.ErrorResponseCodeNotFound, true
	case unavailable(err):
		return 503, api.ErrorResponseCodeUnavailable, true
	}
	return 500, api.ErrorResponseCodeInternal, false
}

func share(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return min(1, max(0, part/whole))
}

func ratio(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// ContextLimit is the context window of a model label (api/openapi.yaml, Derived figures).
func ContextLimit(model string) int64 {
	if strings.HasSuffix(model, "[1m]") {
		return 1_000_000
	}
	return 200_000
}

func sessionState(last, now time.Time) api.SessionState {
	switch age := now.Sub(last); {
	case age < 5*time.Minute:
		return api.SessionStateLive
	case age < time.Hour:
		return api.SessionStateIdle
	}
	return api.SessionStateEnded
}

type totals struct {
	store.UsageTotalsRow
}

func (t totals) tokens() int64 { return t.MainTokens + t.SubTokens }

func (t totals) cost() float64 {
	return t.CostInputUsd + t.CostCacheWriteUsd + t.CostCacheReadUsd + t.CostOutputUsd
}

func (t totals) reread() float64 { return ratio(t.CacheReadTokens, t.ContextTokens) }

func (s *Server) usage(ctx context.Context, w window) (totals, error) {
	r, err := s.q().UsageTotals(ctx, store.UsageTotalsParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	return totals{r}, err
}

// deltas reads the totals of the period and, when there is one, of the previous period.
func (s *Server) deltas(ctx context.Context, p period) (cur totals, prev *totals, err error) {
	if cur, err = s.usage(ctx, p.cur); err != nil || p.prev == nil {
		return cur, nil, err
	}
	pv, err := s.usage(ctx, *p.prev)
	return cur, &pv, err
}

func delta(v float64, prev *totals, f func(totals) float64) api.Delta {
	d := api.Delta{Value: v}
	if prev != nil {
		d.Previous = ptr(f(*prev))
	}
	return d
}

func scopeShare(t totals) api.ScopeShare {
	all := float64(t.tokens())
	return api.ScopeShare{Main: share(float64(t.MainTokens), all), Sub: share(float64(t.SubTokens), all)}
}

func tokenMix(t totals) api.TokenMix {
	return api.TokenMix{Input: t.InputTokens, CacheWrite: t.CacheWriteTokens, CacheRead: t.CacheReadTokens,
		Output: t.OutputTokens}
}

func (s *Server) tokenBands(ctx context.Context) (ctxBands, resultBands []api.TokenBand, err error) {
	rows, err := s.q().ListTokenBands(ctx)
	if err != nil {
		return nil, nil, err
	}
	return bandsOf(rows, store.TokenBandKindContext), bandsOf(rows, store.TokenBandKindResultSize), nil
}

func (s *Server) liveSessions(project, client *string) []api.LiveSession {
	out := []api.LiveSession{}
	if s.Scanner == nil {
		return out
	}
	now := s.now()
	for _, x := range s.Scanner.Sessions() {
		st := sessionState(x.LastAt, now)
		if st == api.SessionStateEnded || (project != nil && *project != x.Project) ||
			(client != nil && *client != string(x.Client)) {
			continue
		}
		out = append(out, api.LiveSession{Client: api.Client(x.Client), Project: x.Project, Session: x.Session,
			Model:     x.Model,
			StartedAt: x.StartedAt.UTC(), LastActivityAt: x.LastAt.UTC(), State: st, ContextTokens: x.ContextTokens,
			ContextLimitTokens: ContextLimitOf(string(x.Client), x.Model), Tokens: x.Tokens, SubagentRuns: x.SubagentRuns})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastActivityAt.After(out[j].LastActivityAt) })
	return out
}

func (s *Server) GetOverview(ctx context.Context, r api.GetOverviewRequestObject) (api.GetOverviewResponseObject,
	error) {
	out, err := s.overview(ctx, r.Params)
	if err == nil {
		return api.GetOverview200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetOverview400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 503:
		logUnavailable("/api/overview", err)
		return api.GetOverview503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

func (s *Server) overview(ctx context.Context, prm api.GetOverviewParams) (api.OverviewResponse, error) {
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.OverviewResponse{}, err
	}
	q := s.q()
	cur, prev, err := s.deltas(ctx, p)
	if err != nil {
		return api.OverviewResponse{}, err
	}
	out := api.OverviewResponse{Period: p.Period, ClaudeOnly: availability(p.cur.claude()),
		Tokens:      delta(float64(cur.tokens()), prev, func(t totals) float64 { return float64(t.tokens()) }),
		CostUsd:     delta(cur.cost(), prev, totals.cost),
		Calls:       delta(float64(cur.Calls), prev, func(t totals) float64 { return float64(t.Calls) }),
		RereadShare: delta(cur.reread(), prev, totals.reread),
		ScopeShare:  scopeShare(cur), TokenMix: tokenMix(cur), Daily: []api.DailyTokens{}, Bands: []api.BandCalls{}}
	if out.RereadTarget, err = q.RereadTarget(ctx); err != nil {
		return out, err
	}
	w := p.cur
	daily, err := q.UsageDaily(ctx, store.UsageDailyParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	if err != nil {
		return out, err
	}
	for _, d := range daily {
		row := api.DailyTokens{Day: date(d.Day), MainTokens: d.MainTokens, SubTokens: d.SubTokens, Calls: d.Calls,
			CostUsd: d.CostUsd, RereadShare: ratio(d.CacheReadTokens, d.ContextTokens)}
		out.Daily = append(out.Daily, row)
		if out.PeakDay == nil || row.MainTokens+row.SubTokens > out.PeakDay.MainTokens+out.PeakDay.SubTokens {
			out.PeakDay = ptr(row)
		}
	}
	if out.ContextBands, _, err = s.tokenBands(ctx); err != nil {
		return out, err
	}
	bands, err := q.ContextBands(ctx, store.ContextBandsParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	if err != nil {
		return out, err
	}
	for _, b := range bands {
		if !w.claude() {
			break
		}
		out.Bands = append(out.Bands, api.BandCalls{Band: api.TokenBand{MinTokens: b.MinTokens, MaxTokens: b.MaxTokens},
			MainCalls: b.MainCalls, SubCalls: b.SubCalls})
	}
	if out.ByClient, err = s.byClient(ctx, w); err != nil {
		return out, err
	}
	out.Unpriced = s.unpriced(w)
	out.LiveSessions = s.liveSessions(w.project, w.client)
	sessions, err := q.SessionsInPeriod(ctx, store.SessionsInPeriodParams{MaxRows: allRows, DayFrom: w.from,
		DayTo: w.to, Project: w.project, Client: w.client})
	if err != nil {
		return out, err
	}
	now := s.now()
	for _, x := range sessions {
		out.SessionMap.Sessions++
		if x.SubagentRuns > 0 {
			out.SessionMap.WithSubagents++
		}
		if x.LastAt.Valid && sessionState(x.LastAt.Time, now) == api.SessionStateLive {
			out.SessionMap.Live++
		}
	}
	out.Compactions, err = s.compactionSummary(ctx, w)
	return out, err
}

// byClient splits the window's tokens and cost per client; the shares add up to the window's tokens.
func (s *Server) byClient(ctx context.Context, w window) ([]api.ClientCost, error) {
	rows, err := s.q().UsageByClient(ctx, store.UsageByClientParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	if err != nil {
		return nil, err
	}
	var all int64
	for _, r := range rows {
		all += r.Tokens
	}
	out := make([]api.ClientCost, 0, len(rows))
	for _, r := range rows {
		out = append(out, api.ClientCost{Client: api.Client(r.Client), Tokens: r.Tokens, CostUsd: r.CostUsd,
			Share: share(float64(r.Tokens), float64(all))})
	}
	return out, nil
}

// costSplit reads each client's totals once a client that records its own cost is in the period; else all stands.
func (s *Server) costSplit(ctx context.Context, w window, all totals, byClient []api.ClientCost) (totals,
	api.RecordedCost, error) {
	if !slices.ContainsFunc(byClient, func(c api.ClientCost) bool { return pricing.RecordsCost(clients.ID(c.Client)) }) {
		return all, api.RecordedCost{Clients: []api.Client{}}, nil
	}
	per := make([]clientTotals, 0, len(byClient))
	for _, c := range byClient {
		name := string(c.Client)
		t, err := s.usage(ctx, window{from: w.from, to: w.to, project: w.project, client: &name})
		if err != nil {
			return totals{}, api.RecordedCost{}, err
		}
		per = append(per, clientTotals{c.Client, t})
	}
	split, rec := splitRecorded(per)
	return split, rec, nil
}

type clientTotals struct {
	client api.Client
	totals
}

// splitRecorded keeps the clients that record their own cost out of the split by kind: that cost is booked whole,
// so leaving it in would show it all as output. Sums, not differences, so no rounding residue shows.
func splitRecorded(per []clientTotals) (totals, api.RecordedCost) {
	var split totals
	rec := api.RecordedCost{Clients: []api.Client{}}
	for _, c := range per {
		t := c.totals
		if pricing.RecordsCost(clients.ID(c.client)) {
			rec.Clients = append(rec.Clients, c.client)
			rec.Tokens += t.InputTokens + t.CacheWriteTokens + t.CacheReadTokens + t.OutputTokens
			rec.CostUsd += t.cost()
			continue
		}
		split.InputTokens += t.InputTokens
		split.CacheWriteTokens += t.CacheWriteTokens
		split.CacheReadTokens += t.CacheReadTokens
		split.OutputTokens += t.OutputTokens
		split.CostInputUsd += t.CostInputUsd
		split.CostCacheWriteUsd += t.CostCacheWriteUsd
		split.CostCacheReadUsd += t.CostCacheReadUsd
		split.CostOutputUsd += t.CostOutputUsd
	}
	return split, rec
}

// unpriced lists what the last scan found without a price; the scan keeps days, not projects.
func (s *Server) unpriced(w window) []api.UnpricedModel {
	out := []api.UnpricedModel{}
	if s.Scanner == nil {
		return out
	}
	for _, p := range s.Scanner.Unpriced(w.from, w.to, w.client) {
		out = append(out, api.UnpricedModel{Client: api.Client(p.Client), Model: p.Model})
	}
	return out
}

func (s *Server) GetTokens(ctx context.Context, r api.GetTokensRequestObject) (api.GetTokensResponseObject, error) {
	out, err := s.tokens(ctx, r.Params)
	if err == nil {
		return api.GetTokens200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetTokens400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 503:
		logUnavailable("/api/tokens", err)
		return api.GetTokens503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

func (s *Server) tokens(ctx context.Context, prm api.GetTokensParams) (api.TokensResponse, error) {
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.TokensResponse{}, err
	}
	q := s.q()
	cur, prev, err := s.deltas(ctx, p)
	if err != nil {
		return api.TokensResponse{}, err
	}
	all := cur.tokens()
	out := api.TokensResponse{Period: p.Period,
		Tokens:           delta(float64(all), prev, func(t totals) float64 { return float64(t.tokens()) }),
		CostUsd:          delta(cur.cost(), prev, totals.cost),
		AvgCostPerDayUsd: cur.cost() / float64(max(1, p.Days)),
		ByModel:          []api.ModelCost{}, Daily: []api.DayCost{}, TopDays: []api.DayCost{}}
	w := p.cur
	if out.ByClient, err = s.byClient(ctx, w); err != nil {
		return out, err
	}
	split, recorded, err := s.costSplit(ctx, w, cur, out.ByClient)
	if err != nil {
		return out, err
	}
	out.Recorded = recorded
	out.ByKind = []api.KindCost{
		{Kind: api.Input, Tokens: split.InputTokens, CostUsd: split.CostInputUsd},
		{Kind: api.CacheWrite, Tokens: split.CacheWriteTokens, CostUsd: split.CostCacheWriteUsd},
		{Kind: api.CacheRead, Tokens: split.CacheReadTokens, CostUsd: split.CostCacheReadUsd},
		{Kind: api.Output, Tokens: split.OutputTokens, CostUsd: split.CostOutputUsd},
	}
	models, err := q.UsageByModel(ctx, store.UsageByModelParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	if err != nil {
		return out, err
	}
	for _, m := range models {
		out.ByModel = append(out.ByModel, api.ModelCost{Model: m.Model, Tokens: m.Tokens, CostUsd: m.CostUsd,
			Share: share(float64(m.Tokens), float64(all))})
	}
	out.Unpriced = s.unpriced(w)
	days, err := q.UsageTopDays(ctx, store.UsageTopDaysParams{MaxRows: allRows, DayFrom: w.from, DayTo: w.to,
		Project: w.project, Client: w.client})
	if err != nil {
		return out, err
	}
	for _, d := range days {
		row := api.DayCost{Day: date(d.Day), Tokens: d.Tokens, CostUsd: d.CostUsd, TopModel: ptr(d.TopModel)}
		out.Daily = append(out.Daily, row)
		if out.PeakCostDay == nil || row.CostUsd > out.PeakCostDay.CostUsd {
			out.PeakCostDay = ptr(row)
		}
	}
	out.TopDays = append(out.TopDays, out.Daily...)
	sort.SliceStable(out.TopDays, func(i, j int) bool { return out.TopDays[i].CostUsd > out.TopDays[j].CostUsd })
	out.TopDays = out.TopDays[:min(len(out.TopDays), maxTopDays)]
	sort.Slice(out.Daily, func(i, j int) bool { return out.Daily[i].Day.Before(out.Daily[j].Day.Time) })
	return out, nil
}

func compactions(rows []store.RecentCompactionsRow) []api.Compaction {
	out := []api.Compaction{}
	for _, c := range rows {
		out = append(out, api.Compaction{At: c.At.Time.UTC(), Project: c.Project, Session: c.Session,
			Scope: api.Scope(c.Scope), Trigger: api.CompactionTrigger(c.Trigger), TokensBefore: c.TokensBefore,
			TokensAfter: c.TokensAfter})
	}
	return out
}

func (s *Server) compactionSummary(ctx context.Context, w window) (api.CompactionSummary, error) {
	if !w.claude() {
		return api.CompactionSummary{ByDay: sized(api.CompactionSummary{}.ByDay, 0), Recent: []api.Compaction{}}, nil
	}
	q := s.q()
	t, err := q.CompactionTotals(ctx, store.CompactionTotalsParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return api.CompactionSummary{}, err
	}
	out := api.CompactionSummary{Count: t.Compactions}
	if t.Compactions > 0 {
		out.AvgFreedTokens = ptr(t.AvgFreedTokens)
	}
	days, err := q.CompactionDays(ctx, store.CompactionDaysParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return out, err
	}
	out.ByDay = make([]struct {
		Count int32              `json:"count"`
		Day   openapi_types.Date `json:"day"`
	}, len(days))
	for i, d := range days {
		out.ByDay[i].Count, out.ByDay[i].Day = d.Compactions, date(d.Day)
	}
	recent, err := q.RecentCompactions(ctx, store.RecentCompactionsParams{DayFrom: w.from, DayTo: w.to,
		Project: w.project, MaxRows: maxCompactions})
	out.Recent = compactions(recent)
	return out, err
}

func (s *Server) GetContext(ctx context.Context, r api.GetContextRequestObject) (api.GetContextResponseObject, error) {
	out, err := s.contextFigures(ctx, r.Params)
	if err == nil {
		return api.GetContext200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetContext400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 422:
		return api.GetContext422JSONResponse{NotForClientJSONResponse: api.NotForClientJSONResponse{Code: code}}, nil
	case ok && st == 503:
		logUnavailable("/api/context", err)
		return api.GetContext503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

func (s *Server) contextFigures(ctx context.Context, prm api.GetContextParams) (api.ContextResponse, error) {
	if err := s.refuseClient(prm.Range, prm.From, prm.To, prm.Client); err != nil {
		return api.ContextResponse{}, err
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.ContextResponse{}, err
	}
	q := s.q()
	cur, prev, err := s.deltas(ctx, p)
	if err != nil {
		return api.ContextResponse{}, err
	}
	perCall := func(t totals) float64 { return ratio(t.ContextTokens, t.Calls) }
	out := api.ContextResponse{Period: p.Period, AvgContextPerCall: delta(perCall(cur), prev, perCall),
		RereadShare: delta(cur.reread(), prev, totals.reread), Bands: []api.ContextBandStats{}}
	if out.RereadTarget, err = q.RereadTarget(ctx); err != nil {
		return out, err
	}
	if out.ContextBands, _, err = s.tokenBands(ctx); err != nil {
		return out, err
	}
	w := p.cur
	bands, err := q.ContextBands(ctx, store.ContextBandsParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	if err != nil {
		return out, err
	}
	for _, b := range bands {
		out.Bands = append(out.Bands, api.ContextBandStats{Band: api.TokenBand{MinTokens: b.MinTokens,
			MaxTokens: b.MaxTokens}, Calls: b.MainCalls + b.SubCalls, ContextTokens: b.ContextTokens,
			CacheReadTokens: b.CacheReadTokens})
	}
	byProject, err := q.PeakContextByProject(ctx, store.PeakContextByProjectParams{DayFrom: w.from, DayTo: w.to,
		Project: w.project, Client: w.client})
	if err != nil {
		return out, err
	}
	out.ByProject = make([]struct {
		Compactions        int32            `json:"compactions"`
		ContextLimitTokens int64            `json:"contextLimitTokens"`
		PeakContextTokens  int64            `json:"peakContextTokens"`
		Project            api.ProjectLabel `json:"project"`
		Sessions           int32            `json:"sessions"`
	}, len(byProject))
	for i, b := range byProject {
		o := &out.ByProject[i]
		o.Project, o.Sessions, o.PeakContextTokens, o.Compactions = b.Project, b.Sessions, b.PeakContextTokens,
			b.Compactions
		o.ContextLimitTokens = ContextLimitOf(b.PeakClient, b.PeakModel)
	}
	daily, err := q.DailyPeakContext(ctx, store.DailyPeakContextParams{DayFrom: w.from, DayTo: w.to,
		Project: w.project, Client: w.client})
	if err != nil {
		return out, err
	}
	out.Daily = make([]struct {
		AvgContextTokens  float64            `json:"avgContextTokens"`
		Compactions       int32              `json:"compactions"`
		Day               openapi_types.Date `json:"day"`
		PeakContextTokens int64              `json:"peakContextTokens"`
		RereadShare       float64            `json:"rereadShare"`
	}, len(daily))
	for i, d := range daily {
		out.Daily[i].Day, out.Daily[i].PeakContextTokens, out.Daily[i].Compactions = date(d.Day),
			d.PeakContextTokens, d.Compactions
		out.Daily[i].RereadShare = ratio(d.CacheReadTokens, d.ContextTokens)
		out.Daily[i].AvgContextTokens = ratio(d.ContextTokens, d.Calls)
	}
	peak, err := q.PeakContext(ctx, store.PeakContextParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	switch {
	case err == nil:
		out.Peak = &struct {
			ContextLimitTokens int64              `json:"contextLimitTokens"`
			ContextTokens      int64              `json:"contextTokens"`
			Day                openapi_types.Date `json:"day"`
			Project            api.ProjectLabel   `json:"project"`
		}{ContextLimitOf(peak.Client, peak.Model), peak.PeakContextTokens, date(peak.Day), peak.Project}
	case !notFound(err):
		return out, err
	}
	out.Compactions, err = s.compactionSummary(ctx, w)
	return out, err
}

func (s *Server) GetSessions(ctx context.Context, r api.GetSessionsRequestObject) (api.GetSessionsResponseObject,
	error) {
	out, err := s.sessions(ctx, r.Params)
	if err == nil {
		return api.GetSessions200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetSessions400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 503:
		logUnavailable("/api/sessions", err)
		return api.GetSessions503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

type projectSessions = struct {
	Project  api.ProjectLabel `json:"project"`
	Sessions int32            `json:"sessions"`
	Share    float64          `json:"share"`
	Tokens   int64            `json:"tokens"`
}

func (s *Server) sessions(ctx context.Context, prm api.GetSessionsParams) (api.SessionsResponse, error) {
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.SessionsResponse{}, err
	}
	w := p.cur
	rows, err := s.q().SessionsInPeriod(ctx, store.SessionsInPeriodParams{MaxRows: allRows, DayFrom: w.from,
		DayTo: w.to, Project: w.project, Client: w.client})
	if err != nil {
		return api.SessionsResponse{}, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastAt.Time.After(rows[j].LastAt.Time) })
	out := api.SessionsResponse{Period: p.Period, ClaudeOnly: availability(w.claude()),
		Sessions: []api.SessionSummary{}, Projects: []projectSessions{}}
	now := s.now()
	byProject := map[string]*projectSessions{}
	var all int64
	for _, x := range rows {
		st := sessionState(x.LastAt.Time, now)
		out.Totals.Sessions++
		out.Totals.Compactions += x.Compactions
		out.Totals.SubagentRuns += x.SubagentRuns
		if st == api.SessionStateLive {
			out.Totals.Live++
		}
		ps := byProject[x.Project]
		if ps == nil {
			ps = &projectSessions{Project: x.Project}
			byProject[x.Project] = ps
		}
		ps.Sessions++
		ps.Tokens += x.Tokens
		all += x.Tokens
		if len(out.Sessions) < maxSessions {
			out.Sessions = append(out.Sessions, api.SessionSummary{Project: x.Project, Session: x.Session,
				Client: api.Client(x.Client), Model: x.Model, StartedAt: x.FirstAt.Time.UTC(),
				LastActivityAt: x.LastAt.Time.UTC(), State: st, Calls: x.Calls, Tokens: x.Tokens,
				PeakContextTokens: x.PeakContextTokens, ContextLimitTokens: ContextLimitOf(x.Client, x.Model),
				CostUsd: x.CostUsd, SubagentRuns: x.SubagentRuns,
				Compactions: x.Compactions})
		}
	}
	for _, ps := range byProject {
		ps.Share = share(float64(ps.Tokens), float64(all))
		out.Projects = append(out.Projects, *ps)
	}
	sort.Slice(out.Projects, func(i, j int) bool {
		a, b := out.Projects[i], out.Projects[j]
		return a.Tokens > b.Tokens || a.Tokens == b.Tokens && a.Project < b.Project
	})
	out.Compactions, err = s.compactionSummary(ctx, w)
	return out, err
}

func (s *Server) GetSession(ctx context.Context, r api.GetSessionRequestObject) (api.GetSessionResponseObject, error) {
	out, err := s.session(ctx, r.Params.Project, r.Session, r.Params.Client)
	if err == nil {
		return api.GetSession200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 404:
		return api.GetSession404JSONResponse{NotFoundJSONResponse: api.NotFoundJSONResponse{Code: code}}, nil
	case ok && st == 503:
		logUnavailable("/api/sessions/{session}", err)
		return api.GetSession503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

func (s *Server) session(ctx context.Context, project, session string, client *api.Client) (api.SessionDetailResponse,
	error) {
	if s.DB == nil {
		return api.SessionDetailResponse{}, errNoDB
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	q := s.q()
	sum, err := q.SessionSummaryByKey(ctx, store.SessionSummaryByKeyParams{Project: project, Session: session,
		Client: clientParam(client)})
	if err != nil {
		return api.SessionDetailResponse{}, err
	}
	mix, err := q.SessionTokenMix(ctx, store.SessionTokenMixParams{Project: project, Session: session,
		Client: clientParam(client)})
	if err != nil {
		return api.SessionDetailResponse{}, err
	}
	claude := sum.Client == string(clients.ClaudeCode)
	if !claude {
		sum.SubagentRuns, sum.Compactions = 0, 0
	}
	out := api.SessionDetailResponse{ClaudeOnly: availability(claude), ContextTokens: sum.LastContextTokens,
		TokenMix: api.TokenMix{Input: mix.InputTokens, CacheWrite: mix.CacheWriteTokens,
			CacheRead: mix.CacheReadTokens, Output: mix.OutputTokens},
		Summary: api.SessionSummary{Project: project, Session: session, Client: api.Client(sum.Client),
			Model:     sum.Model,
			StartedAt: sum.FirstAt.Time.UTC(), LastActivityAt: sum.LastAt.Time.UTC(),
			State: sessionState(sum.LastAt.Time, s.now()), Calls: sum.Calls, Tokens: sum.Tokens,
			PeakContextTokens: sum.PeakContextTokens, ContextLimitTokens: ContextLimitOf(sum.Client, sum.Model),
			CostUsd:      sum.CostUsd,
			SubagentRuns: sum.SubagentRuns, Compactions: sum.Compactions}}
	if !claude {
		out.Subagents, out.Compactions = sized(out.Subagents, 0), []api.Compaction{}
		return out, nil
	}
	subs, err := q.SessionSubagents(ctx, store.SessionSubagentsParams{Project: project, Session: session})
	if err != nil {
		return out, err
	}
	out.Subagents = make([]struct {
		AgentType api.AgentTypeLabel `json:"agentType"`
		Runs      int32              `json:"runs"`
		Tokens    int64              `json:"tokens"`
	}, len(subs))
	for i, x := range subs {
		out.Subagents[i].AgentType, out.Subagents[i].Runs, out.Subagents[i].Tokens = x.AgentType, x.Runs, x.Tokens
	}
	comp, err := q.SessionCompactions(ctx, store.SessionCompactionsParams{Project: project, Session: session,
		MaxRows: maxCompactions})
	if err != nil {
		return out, err
	}
	recent := make([]store.RecentCompactionsRow, len(comp))
	for i, c := range comp {
		recent[i] = store.RecentCompactionsRow(c)
	}
	out.Compactions = compactions(recent)
	return out, nil
}

func (s *Server) GetSubagents(ctx context.Context, r api.GetSubagentsRequestObject) (api.GetSubagentsResponseObject,
	error) {
	out, err := s.subagents(ctx, r.Params)
	if err == nil {
		return api.GetSubagents200JSONResponse(out), nil
	}
	switch st, code, ok := failure(err); {
	case ok && st == 400:
		return api.GetSubagents400JSONResponse{BadRequestJSONResponse: api.BadRequestJSONResponse{Code: code}}, nil
	case ok && st == 422:
		return api.GetSubagents422JSONResponse{NotForClientJSONResponse: api.NotForClientJSONResponse{Code: code}}, nil
	case ok && st == 503:
		logUnavailable("/api/subagents", err)
		return api.GetSubagents503JSONResponse{UnavailableJSONResponse: api.UnavailableJSONResponse{Code: code}}, nil
	}
	return nil, err
}

// lastDays is the window of the last n days of a period, for the per-day charts.
func lastDays(w window, n int) window {
	if from := w.to.AddDate(0, 0, 1-n); from.After(w.from) {
		w.from = from
	}
	return w
}

func (s *Server) subagents(ctx context.Context, prm api.GetSubagentsParams) (api.SubagentsResponse, error) {
	if err := s.refuseClient(prm.Range, prm.From, prm.To, prm.Client); err != nil {
		return api.SubagentsResponse{}, err
	}
	ctx, cancel := s.dbCtx(ctx)
	defer cancel()
	p, err := s.resolve(ctx, prm.Range, prm.From, prm.To, prm.Project, prm.Client)
	if err != nil {
		return api.SubagentsResponse{}, err
	}
	q := s.q()
	cur, err := s.usage(ctx, p.cur)
	if err != nil {
		return api.SubagentsResponse{}, err
	}
	runs := func(w window) (int64, error) {
		return q.SubagentRunsTotal(ctx, store.SubagentRunsTotalParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	}
	n, err := runs(p.cur)
	if err != nil {
		return api.SubagentsResponse{}, err
	}
	out := api.SubagentsResponse{Period: p.Period, Runs: api.Delta{Value: float64(n)}, ScopeShare: scopeShare(cur),
		CostUsd: cur.SubCostUsd, TotalCostUsd: cur.cost(), Types: []api.AgentTypeStats{}}
	if p.prev != nil {
		pn, err := runs(*p.prev)
		if err != nil {
			return out, err
		}
		out.Runs.Previous = ptr(float64(pn))
	}
	if out.RereadTarget, err = q.RereadTarget(ctx); err != nil {
		return out, err
	}
	w := p.cur
	types, err := q.SubagentTypes(ctx, store.SubagentTypesParams{DayFrom: w.from, DayTo: w.to, Project: w.project})
	if err != nil {
		return out, err
	}
	for _, t := range types {
		avg := func(v int64) int64 {
			if t.Runs <= 0 {
				return 0
			}
			return v / t.Runs
		}
		out.Types = append(out.Types, api.AgentTypeStats{AgentType: t.AgentType, TopModel: t.TopModel, Runs: t.Runs,
			Calls: t.Calls, Tokens: t.Tokens, CacheReadTokens: t.CacheReadTokens, ContextTokens: t.ContextTokens,
			AvgFirstContextTokens: avg(t.FirstContextTokens), AvgPeakContextTokens: avg(t.PeakContextTokens),
			CostUsd: t.CostUsd})
	}
	recent := lastDays(w, recentDays)
	daily, err := q.SubagentRunsDaily(ctx, store.SubagentRunsDailyParams{DayFrom: recent.from, DayTo: recent.to,
		Project: w.project})
	if err != nil {
		return out, err
	}
	out.Daily = make([]struct {
		AgentType api.AgentTypeLabel `json:"agentType"`
		Day       openapi_types.Date `json:"day"`
		Runs      int32              `json:"runs"`
	}, len(daily))
	for i, d := range daily {
		out.Daily[i].AgentType, out.Daily[i].Day, out.Daily[i].Runs = d.AgentType, date(d.Day), d.Runs
	}
	scope, err := q.UsageDaily(ctx, store.UsageDailyParams{DayFrom: w.from, DayTo: w.to, Project: w.project,
		Client: w.client})
	if err != nil {
		return out, err
	}
	out.ScopeDaily = make([]struct {
		Day        openapi_types.Date `json:"day"`
		SubCostUsd float64            `json:"subCostUsd"`
		SubShare   float64            `json:"subShare"`
	}, len(scope))
	for i, d := range scope {
		out.ScopeDaily[i].Day, out.ScopeDaily[i].SubCostUsd = date(d.Day), d.SubCostUsd
		out.ScopeDaily[i].SubShare = share(float64(d.SubTokens), float64(d.MainTokens+d.SubTokens))
	}
	return out, nil
}
