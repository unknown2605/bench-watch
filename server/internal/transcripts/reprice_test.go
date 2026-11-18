package transcripts

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/pricing"
)

const soloModel = "gpt-6.1-sol"

func keptRow(project, session string, isUse bool, s pricing.Stored, was float64) repriceRow {
	g := repriceGroup{day: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), project: project, scope: "main",
		model: soloModel, client: clients.Codex}
	return repriceRow{group: g, session: session, isUse: isUse, sums: s, was: pricing.Cost{Output: was}}
}

func TestStaleClientsRepriceOnlyAnOlderVersion(t *testing.T) {
	prices := map[clients.ID]int{}
	for _, c := range clients.All() {
		prices[c] = 2
	}
	rec := dayRecord{Prices: map[clients.ID]int{clients.Codex: 3, clients.GeminiCLI: 2, clients.QwenCode: 1}}
	stale := staleClients(rec, prices)
	if contains(stale, clients.Codex) || contains(stale, clients.GeminiCLI) {
		t.Errorf("a downgrade or an equal version repriced: %v", stale)
	}
	if !contains(stale, clients.QwenCode) || !contains(stale, clients.CopilotCLI) {
		t.Errorf("version 1, recorded or missing, not repriced to 2: %v", stale)
	}
}

func contains(ids []clients.ID, id clients.ID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// A row that cannot be priced exactly keeps a stored non-zero cost and is not listed; a zero one stays and is listed.
func TestNotExactKeptRowKeepsItsStoredCost(t *testing.T) {
	tiered := pricing.Stored{Calls: 3, Input: 400000, Output: 1000}
	changed, unpriced := decideReprice([]repriceRow{
		keptRow("p", "", true, tiered, 0.63), keptRow("p", "0a1b2c3d", false, tiered, 0.63)})
	if len(changed) != 0 || len(unpriced) != 0 {
		t.Fatalf("priced row changed %+v, unpriced %+v", changed, unpriced)
	}
	changed, unpriced = decideReprice([]repriceRow{
		keptRow("p", "", true, tiered, 0), keptRow("p", "0a1b2c3d", false, tiered, 0)})
	day := keptRow("p", "", true, tiered, 0).group.day
	want := []aggregate.UnpricedPair{{Client: string(clients.Codex), Model: soloModel}}
	if len(changed) != 0 || fmt.Sprint(unpriced[day]) != fmt.Sprint(want) {
		t.Fatalf("zero row changed %+v, unpriced %+v", changed, unpriced)
	}
}

// Repeating a reprice on rows already at today's prices writes nothing.
func TestRepriceAtTodaysPricesWritesNothing(t *testing.T) {
	s := pricing.Stored{Calls: 2, Input: 100000, CacheRead: 50000, Output: 20000}
	c := pricing.CostOf(clients.Codex, soloModel, pricing.Usage{Input: s.Input, CacheRead: s.CacheRead, Output: s.Output})
	use := keptRow("p", "", true, s, 0)
	use.was = pricing.Cost{Input: c.Input, CacheRead: c.CacheRead, Output: c.Output + 4e-7}
	sess := keptRow("p", "0a1b2c3d", false, s, total(c))
	if changed, unpriced := decideReprice([]repriceRow{use, sess}); len(changed) != 0 || len(unpriced) != 0 {
		t.Fatalf("changed %+v unpriced %+v", changed, unpriced)
	}
	use.was, sess.was = pricing.Cost{}, pricing.Cost{}
	changed, _ := decideReprice([]repriceRow{use, sess})
	if len(changed) != 2 || !within(total(changed[0].cost), total(c)) || !within(changed[1].cost.Output, total(c)) {
		t.Fatalf("unpriced rows not repriced: %+v", changed)
	}
}

// One decision per group: a usage row that sums calls over the first tier holds both tables back, even when each
// session row alone would price exactly.
func TestBothTablesFollowOneDecisionPerGroup(t *testing.T) {
	one := pricing.Stored{Calls: 1, Input: 200000, Output: 10}
	sum := pricing.Stored{Calls: 2, Input: 400000, Output: 20}
	rows := []repriceRow{keptRow("p", "", true, sum, 0), keptRow("p", "0a1b2c3d", false, one, 0),
		keptRow("p", "1a1b2c3d", false, one, 0), keptRow("q", "", true, one, 0), keptRow("q", "0a1b2c3d", false, one, 0)}
	changed, _ := decideReprice(rows)
	if len(changed) != 2 {
		t.Fatalf("changed %d rows, want only group q's 2", len(changed))
	}
	for _, r := range changed {
		if r.group.project != "q" {
			t.Errorf("group %s repriced in one table only: %+v", r.group.project, r)
		}
	}
}

func TestRepriceReplacesTheRepricedClientsPairs(t *testing.T) {
	old := []aggregate.UnpricedPair{{Client: "codex", Model: soloModel}, {Client: "claude-code", Model: "x"}}
	got := replacePairs(old, []clients.ID{clients.Codex}, nil)
	if fmt.Sprint(got) != fmt.Sprint([]aggregate.UnpricedPair{{Client: "claude-code", Model: "x"}}) {
		t.Fatalf("pairs %+v", got)
	}
	got = replacePairs(old, []clients.ID{clients.Codex}, []aggregate.UnpricedPair{{Client: "codex", Model: "y"}})
	if len(got) != 2 || got[1].Model != "y" {
		t.Fatalf("pairs %+v", got)
	}
}

func TestStatusUnpricedCountsKeptDayPairs(t *testing.T) {
	pair := aggregate.UnpricedPair{Client: "codex", Model: soloModel}
	byDay := map[time.Time][]aggregate.UnpricedPair{{}: {pair}}
	days := map[string]dayRecord{"2026-05-01": {Unpriced: []aggregate.UnpricedPair{pair, {Client: "codex", Model: "y"}}},
		"2026-05-02": {Unpriced: []aggregate.UnpricedPair{{Client: "codex", Model: "y"}}}}
	if got := unpricedCount(1, byDay, days); got != 2 {
		t.Fatalf("unpriced %d, want 2", got)
	}
}

func TestRepriceErrorHasItsOwnCode(t *testing.T) {
	if got := codeOf(repriceError{errRefused}); got != "EREPRICE_EPG_57P03" {
		t.Fatalf("code %q", got)
	}
	if got := codeOf(repriceError{errors.New("x")}); got != "EREPRICE_EOTHER" {
		t.Fatalf("code %q", got)
	}
}
