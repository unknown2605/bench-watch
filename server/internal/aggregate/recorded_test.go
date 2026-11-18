package aggregate

import (
	"testing"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/store"
)

func recorded(id, model string, usd float64) extract.Event {
	e := callOf(id, model)
	e.USD, e.HasUSD = usd, true
	return e
}

func recordedShards() ShardSource {
	return shardsOf(
		&scan.Shard{Key: "copilot-cli/a", Client: clients.CopilotCLI, Project: "p", Session: "s", Scope: "main",
			Events: []extract.Event{recorded("c1", "gpt-5", 0.25), recorded("c2", "gpt-5", 0), callOf("c3", "gpt-5")}},
		&scan.Shard{Key: "copilot-cli/b", Client: clients.CopilotCLI, Project: "p", Session: "t", Scope: "main",
			Events: []extract.Event{recorded("c1", "gpt-5", 0.5), callOf("c2", "gpt-5")}},
		&scan.Shard{Key: "opencode/c", Client: clients.OpenCode, Project: "p", Session: "u", Scope: "main",
			Events: []extract.Event{recorded("o1", "kimi-k2", 1.5)}},
	)
}

func TestRecordedCostInRows(t *testing.T) {
	rows, err := BuildRows(recordedShards(), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cost := map[store.ClientID]float64{}
	for _, u := range rows.Usage {
		if u.CostInputUsd+u.CostCacheWriteUsd+u.CostCacheReadUsd != 0 {
			t.Errorf("recorded cost outside the output column: %+v", u)
		}
		cost[u.Client] += u.CostOutputUsd
	}
	// c1 counts once at its larger copy, c2's recorded 0 is a price, c3 has none.
	if cost[store.ClientIDCopilotCli] != 0.5 || cost[store.ClientIDOpencode] != 1.5 {
		t.Errorf("recorded cost per client: %v", cost)
	}
	if rows.Unpriced != 1 {
		t.Errorf("unpriced %d, want 1: copilot gpt-5 has a call with no recorded cost", rows.Unpriced)
	}
	if rows.UniqueCalls != 4 {
		t.Errorf("unique calls %d, want 4", rows.UniqueCalls)
	}
}

func TestRecordedCostInData(t *testing.T) {
	d, err := Aggregate(recordedShards(), MaxRunItems)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Unpriced) != 1 || d.Unpriced[0] != "gpt-5" || d.UnpricedClients[0] != string(clients.CopilotCLI) {
		t.Errorf("unpriced %v %v", d.Unpriced, d.UnpricedClients)
	}
	total := 0.0
	for _, row := range d.Usage {
		total += row[len(row)-1]
	}
	if total != 2 {
		t.Errorf("output cost %v, want 2", total)
	}
}

func TestRecordedWithoutCostIsUnpriced(t *testing.T) {
	rows, err := BuildRows(shardsOf(&scan.Shard{Key: "opencode/x", Client: clients.OpenCode, Project: "p", Session: "s",
		Scope: "main", Events: []extract.Event{callOf("x1", "kimi-k2")}}), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Unpriced != 1 || rows.Usage[0].CostOutputUsd != 0 {
		t.Errorf("unpriced %d, usage %+v", rows.Unpriced, rows.Usage)
	}
}

func TestUnpricedByDayFeedsThePeriodList(t *testing.T) {
	rows, err := BuildRows(recordedShards(), MaxRunItems, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	byDay := rows.UnpricedByDay()
	day := rows.Days()[0]
	all := UnpricedIn(byDay, day, day, nil)
	if len(all) != 1 || all[0] != (UnpricedPair{"copilot-cli", "gpt-5"}) {
		t.Errorf("unpriced %v, want copilot-cli gpt-5 once: opencode recorded a cost", all)
	}
	other := "opencode"
	if got := UnpricedIn(byDay, day, day, &other); len(got) != 0 {
		t.Errorf("client filter: %v", got)
	}
	if got := UnpricedIn(byDay, day.AddDate(0, 0, 1), day.AddDate(0, 0, 2), nil); got == nil || len(got) != 0 {
		t.Errorf("a window without the day: %v, want an empty list", got)
	}
}
