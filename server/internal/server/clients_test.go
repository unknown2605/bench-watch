package server

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"testing"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/api"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/transcripts"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClientListMergesDetectedAndStoredClients(t *testing.T) {
	cases := []struct {
		name     string
		detected []clients.ID
		stored   []string
		want     string
	}{
		{"none", nil, nil, "[]"},
		{"detected only", []clients.ID{clients.Codex}, nil, "[{codex true}]"},
		{"rows only", nil, []string{"gemini-cli"}, "[{gemini-cli false}]"},
		{"both, listed once in client order", []clients.ID{clients.Codex, clients.ClaudeCode},
			[]string{"opencode", "claude-code"}, "[{claude-code true} {codex true} {opencode false}]"},
	}
	for _, c := range cases {
		got := clientList(c.detected, c.stored)
		if got == nil || fmt.Sprint(got) != c.want {
			t.Errorf("%s: %v, want %s", c.name, got, c.want)
		}
	}
	for _, id := range clients.All() {
		if want := id == clients.ClaudeCode || id == clients.Codex; id.Verified() != want {
			t.Errorf("%s verified %v, want %v", id, id.Verified(), want)
		}
	}
}

// The refusal comes before the database, so it holds while history is paused, and a bad period stays a 400.
func TestClaudeOnlyTabsRefuseOtherClients(t *testing.T) {
	_, h := newServer(t, nil, nil)
	for _, tab := range []string{"context", "subagents", "fills", "guard"} {
		for _, c := range []string{"codex", "opencode", "gemini-cli", "qwen-code", "copilot-cli"} {
			rec := get(h, AllowedHosts[0], "/api/"+tab+"?client="+c)
			if rec.Code != http.StatusUnprocessableEntity || code(t, rec) != "not_for_client" {
				t.Errorf("%s client %s: %d %s, want 422 not_for_client", tab, c, rec.Code, rec.Body.String())
			}
		}
		if rec := get(h, AllowedHosts[0], "/api/"+tab+"?client=claude-code"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s client claude-code: %d, want 503 (no refusal)", tab, rec.Code)
		}
		if rec := get(h, AllowedHosts[0], "/api/"+tab+"?client=codex&from=2026-01-01"); rec.Code != http.StatusBadRequest ||
			code(t, rec) != "bad_date" {
			t.Errorf("%s bad period with codex: %d, want 400 bad_date", tab, rec.Code)
		}
	}
	for _, tab := range []string{"overview", "tokens", "sessions", "sessions/abcd1234?project=benchwatch&"} {
		target := "/api/" + tab
		if tab[len(tab)-1] == '&' {
			target += "client=codex"
		} else {
			target += "?client=codex"
		}
		if rec := get(h, AllowedHosts[0], target); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503: a usage tab never refuses a client", target, rec.Code)
		}
	}
}

func TestContextLimitIsClaudeOnly(t *testing.T) {
	if got := ContextLimitOf("claude-code", "claude-opus-5-5[1m]"); got != 1_000_000 {
		t.Errorf("claude 1m: %d", got)
	}
	if got := ContextLimitOf("claude-code", "claude-opus-5-5"); got != 200_000 {
		t.Errorf("claude: %d", got)
	}
	for _, c := range []string{"codex", "copilot-cli"} {
		if got := ContextLimitOf(c, "claude-sonnet-4.5"); got != 0 {
			t.Errorf("%s: %d, want 0", c, got)
		}
	}
	if !claudeFigures(nil) || !claudeFigures(ptr(api.ClaudeCode)) || claudeFigures(ptr(api.Codex)) {
		t.Error("claudeFigures: want true for no filter and claude-code only")
	}
}

func writeTwoClients(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	d1 := noonDaysAgo(1)
	codex := &scan.Shard{Client: clients.Codex, Project: "benchwatch", Session: "ef015678", Scope: "main"}
	for i, id := range []string{"x1", "x2", "x3"} {
		codex.Events = append(codex.Events, extract.Event{Kind: extract.Call, Ts: d1 + int64(i), ID: id,
			Text: "gpt-5.1-codex-max", N: [5]int64{100, 0, 0, 1000, 50}})
	}
	ctx := context.Background()
	cb, rb, err := transcripts.Bands(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := aggregate.BuildRows(func(visit func(*scan.Shard) error) error {
		for _, s := range append(shards(), codex) {
			if err := visit(s); err != nil {
				return err
			}
		}
		return nil
	}, aggregate.MaxRunItems, cb, rb)
	if err != nil {
		t.Fatal(err)
	}
	if err := transcripts.WriteDays(ctx, pool, rows, rows.Days()); err != nil {
		t.Fatalf("write transcripts: %v", err)
	}
}

// The Claude fixture holds 6 calls (4 main, 2 sub) and the Codex shard 3; no filter adds both.
func TestClientFilterSplitsUsage(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	writeTwoClients(t, pool)
	_, h := newServer(t, pool, nil)
	for _, c := range []struct {
		query  string
		calls  float64
		claude bool
		models string
	}{
		{"", 9, true, "claude-haiku-5-5 claude-opus-5-5 claude-opus-5-5[1m] gpt-5.1-codex-max"},
		{"&client=codex", 3, false, "gpt-5.1-codex-max"},
		{"&client=claude-code", 6, true, "claude-haiku-5-5 claude-opus-5-5 claude-opus-5-5[1m]"},
	} {
		ov := decode[api.OverviewResponse](t, h, "/api/overview?range=7d"+c.query)
		if ov.Calls.Value != c.calls || ov.ClaudeOnly.Available != c.claude {
			t.Errorf("overview%s: calls %v claudeOnly %v", c.query, ov.Calls.Value, ov.ClaudeOnly.Available)
		}
		if !c.claude && (len(ov.Bands) != 0 || ov.Compactions.Count != 0) {
			t.Errorf("overview%s: Claude-only figures under another client: %+v %+v", c.query, ov.Bands, ov.Compactions)
		}
		tk := decode[api.TokensResponse](t, h, "/api/tokens?range=7d"+c.query)
		var models []string
		for _, m := range tk.ByModel {
			models = append(models, string(m.Model))
		}
		sort.Strings(models)
		if strings.Join(models, " ") != c.models {
			t.Errorf("tokens%s byModel: %v, want %s", c.query, models, c.models)
		}
		ss := decode[api.SessionsResponse](t, h, "/api/sessions?range=7d"+c.query)
		for _, s := range ss.Sessions {
			want := map[string]api.Client{"abcd1234": api.ClaudeCode, "ef015678": api.Codex}[string(s.Session)]
			if s.Client != want || (s.Client == api.Codex && (s.ContextLimitTokens != 0 || s.SubagentRuns != 0)) {
				t.Errorf("sessions%s: %+v", c.query, s)
			}
		}
		if ss.ClaudeOnly.Available != c.claude {
			t.Errorf("sessions%s claudeOnly %v", c.query, ss.ClaudeOnly.Available)
		}
	}
	meta := decode[api.MetaResponse](t, h, "/api/meta")
	if fmt.Sprint(meta.Clients) != "[{claude-code true} {codex true}]" {
		t.Errorf("meta clients from stored rows: %v", meta.Clients)
	}
}

// The split follows the other filters, sorts by tokens and adds up to the totals; nothing is unpriced without a scan.
func TestByClientAddsUpToTotals(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	writeTwoClients(t, pool)
	_, h := newServer(t, pool, nil)
	ov := decode[api.OverviewResponse](t, h, "/api/overview?range=7d")
	tk := decode[api.TokensResponse](t, h, "/api/tokens?range=7d")
	for name, got := range map[string]struct {
		split  []api.ClientCost
		tokens float64
		cost   float64
	}{
		"overview": {ov.ByClient, ov.Tokens.Value, ov.CostUsd.Value},
		"tokens":   {tk.ByClient, tk.Tokens.Value, tk.CostUsd.Value},
	} {
		var tokens int64
		var cost, shares float64
		for i, c := range got.split {
			tokens += c.Tokens
			cost += c.CostUsd
			shares += c.Share
			if i > 0 && c.Tokens > got.split[i-1].Tokens {
				t.Errorf("%s: not sorted by tokens: %+v", name, got.split)
			}
		}
		if len(got.split) != 2 || float64(tokens) != got.tokens || math.Abs(cost-got.cost) > 1e-9 ||
			math.Abs(shares-1) > 1e-9 {
			t.Errorf("%s: split %+v vs tokens %v cost %v", name, got.split, got.tokens, got.cost)
		}
	}
	one := decode[api.TokensResponse](t, h, "/api/tokens?range=7d&client=codex")
	if len(one.ByClient) != 1 || one.ByClient[0].Client != api.Codex || one.ByClient[0].Share != 1 {
		t.Errorf("client filter: %+v", one.ByClient)
	}
	if ov.Unpriced == nil || len(ov.Unpriced) != 0 || tk.Unpriced == nil {
		t.Errorf("unpriced without a scanner: %v %v, want empty lists", ov.Unpriced, tk.Unpriced)
	}
}
