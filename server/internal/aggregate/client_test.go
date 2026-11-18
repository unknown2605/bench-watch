package aggregate

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/store"
)

func shardsOf(list ...*scan.Shard) ShardSource {
	return func(visit func(*scan.Shard) error) error {
		for _, s := range list {
			if err := visit(s); err != nil {
				return err
			}
		}
		return nil
	}
}

var clientNoon = time.Date(2026, 10, 10, 12, 0, 0, 0, time.Local).Unix()

func callOf(id, model string) extract.Event {
	return extract.Event{Kind: extract.Call, Ts: clientNoon, ID: id, Text: model, N: [5]int64{10, 0, 0, 100, 5}}
}

func TestRowsSplitByClient(t *testing.T) {
	rows, err := BuildRows(shardsOf(
		&scan.Shard{Key: "claude-code/a", Client: clients.ClaudeCode, Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{callOf("a1", "claude-opus-5-5")}},
		&scan.Shard{Key: "codex/b", Client: clients.Codex, Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{callOf("b1", "gpt-5.1-codex-max"), callOf("b2", "claude-opus-5-5")}},
	), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[store.ClientID][]string{}
	for _, u := range rows.Usage {
		got[u.Client] = append(got[u.Client], u.Model)
		if u.Client == store.ClientIDCodex && u.CostInputUsd+u.CostOutputUsd+u.CostCacheReadUsd != 0 {
			t.Errorf("codex row priced: %+v", u)
		}
	}
	if len(got[store.ClientIDClaudeCode]) != 1 || len(got[store.ClientIDCodex]) != 2 {
		t.Fatalf("usage by client: %v", got)
	}
	if rows.Unpriced != 2 {
		t.Errorf("unpriced models %d, want the two codex models", rows.Unpriced)
	}
	if len(rows.ContextBands) != 2 || rows.ContextBands[0].Client == rows.ContextBands[1].Client {
		t.Errorf("one band row per client expected: %+v", rows.ContextBands)
	}
	if len(rows.Sessions) != 3 {
		t.Errorf("sessions: %d rows, want 3", len(rows.Sessions))
	}
	codexOnly, err := BuildRows(shardsOf(&scan.Shard{Key: "codex/b", Client: clients.Codex, Project: "p", Session: "s",
		Scope: "main", Events: []extract.Event{callOf("a1", "claude-opus-5-5")}}), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	claudeOnly, err := BuildRows(shardsOf(&scan.Shard{Key: "claude-code/a", Client: clients.ClaudeCode, Project: "p",
		Session: "s", Scope: "main", Events: []extract.Event{callOf("a1", "unknown")}}), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	day := rows.Usage[0].Day
	if codexOnly.Digests()[day] == claudeOnly.Digests()[day] {
		t.Error("the same row under two clients gives one digest")
	}
}

func TestNonClaudeShardsRefused(t *testing.T) {
	agent := "worker"
	for name, shard := range map[string]*scan.Shard{
		"sub scope": {Key: "codex/x", Client: clients.Codex, Project: "p", Session: "s", Scope: "sub",
			AgentType: &agent, Events: []extract.Event{callOf("x1", "gpt-5")}},
		"result event": {Key: "codex/x", Client: clients.Codex, Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{callOf("x1", "gpt-5"),
				{Kind: extract.Result, Ts: clientNoon, ID: "0123456789abcdef", Text: "Read: whole"}}},
		"no client": {Key: "x", Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{callOf("x1", "claude-opus-5-5")}},
	} {
		if _, err := BuildRows(shardsOf(shard), MaxRunItems, nil, nil); !errors.Is(err, ErrClientRefused) {
			t.Errorf("%s: err %v, want ErrClientRefused", name, err)
		}
	}
}

func TestAggregateKeysUnpricedByClientAndModel(t *testing.T) {
	data, err := Aggregate(shardsOf(
		&scan.Shard{Key: "claude-code/a", Client: clients.ClaudeCode, Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{callOf("a1", "claude-mystery-9"), callOf("a2", "claude-mystery-9"),
				callOf("a3", "claude-opus-5-5")}},
		&scan.Shard{Key: "codex/b", Client: clients.Codex, Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{callOf("b1", "claude-mystery-9"), callOf("b2", "gpt-5.1-codex-max")}},
	), MaxRunItems)
	if err != nil {
		t.Fatal(err)
	}
	type pair struct{ client, model string }
	var got []pair
	for i, m := range data.Unpriced {
		got = append(got, pair{data.UnpricedClients[i], m})
	}
	want := []pair{{"claude-code", "claude-mystery-9"}, {"codex", "claude-mystery-9"}, {"codex", "gpt-5.1-codex-max"}}
	if len(data.UnpricedClients) != len(data.Unpriced) || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("unpriced %v clients %v, want %v", data.Unpriced, data.UnpricedClients, want)
	}
}

func TestCodexCallsArePricedUpToTheShortContextLimit(t *testing.T) {
	long := extract.Event{Kind: extract.Call, Ts: clientNoon, ID: "c2", Text: "gpt-5.5", N: [5]int64{272001, 0, 0, 0, 5}}
	rows, err := BuildRows(shardsOf(&scan.Shard{Key: "codex/c", Client: clients.Codex, Project: "p", Session: "s",
		Scope: "main", Events: []extract.Event{callOf("c1", "gpt-5.5"), long}}), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Usage) != 1 {
		t.Fatalf("usage rows %+v", rows.Usage)
	}
	u := rows.Usage[0]
	// Only c1 is priced: 10 input at $5, 100 cached at $0.5, 5 output at $30 per 1M.
	if !near(u.CostInputUsd, 10*5/1e6) || !near(u.CostCacheReadUsd, 100*0.5/1e6) || !near(u.CostOutputUsd, 5*30/1e6) {
		t.Errorf("codex costs %+v", u)
	}
	if rows.Unpriced != 1 {
		t.Errorf("unpriced %d, want the call above 272K input", rows.Unpriced)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
