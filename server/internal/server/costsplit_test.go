package server

import (
	"fmt"
	"testing"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/store"
)

func usageOf(in, cw, cr, out int64, cin, ccw, ccr, cout float64) totals {
	return totals{store.UsageTotalsRow{InputTokens: in, CacheWriteTokens: cw, CacheReadTokens: cr, OutputTokens: out,
		CostInputUsd: cin, CostCacheWriteUsd: ccw, CostCacheReadUsd: ccr, CostOutputUsd: cout}}
}

func TestRecordedCostStaysOutOfTheSplitByKind(t *testing.T) {
	split, rec := splitRecorded([]clientTotals{
		{api.ClaudeCode, usageOf(100, 10, 1000, 50, 1, 0.5, 0.25, 2)},
		{api.CopilotCli, usageOf(400, 0, 0, 40, 0, 0, 0, 0.3)},
		{api.Codex, usageOf(20, 0, 200, 5, 0.1, 0, 0.01, 0.15)},
		{api.Opencode, usageOf(60, 0, 0, 6, 0, 0, 0, 0.2)},
	})
	want := usageOf(120, 10, 1200, 55, 1.1, 0.5, 0.26, 2.15)
	if split != want {
		t.Errorf("split %+v, want %+v: recorded clients left in", split, want)
	}
	if fmt.Sprint(rec.Clients) != "[copilot-cli opencode]" || rec.Tokens != 506 || rec.CostUsd != 0.5 {
		t.Errorf("recorded %+v", rec)
	}
	none, rec := splitRecorded([]clientTotals{{api.CopilotCli, usageOf(400, 0, 0, 40, 0, 0, 0, 0.3)}})
	if none != (totals{}) || rec.CostUsd != 0.3 {
		t.Errorf("recorded only: split %+v recorded %+v; want an empty split", none, rec)
	}
	if _, rec := splitRecorded(nil); rec.Clients == nil || len(rec.Clients) != 0 {
		t.Errorf("no clients: %+v, want an empty list", rec)
	}
}
