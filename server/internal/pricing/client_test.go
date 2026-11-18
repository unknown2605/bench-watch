package pricing

import (
	"math"
	"testing"

	"benchwatch/server/internal/clients"
)

func TestClientsWithoutPricesAreNeverFree(t *testing.T) {
	u := Usage{Input: 1e6, Output: 1e6}
	if c := CostOf(clients.ClaudeCode, "claude-opus-5-5", u); c.Basis != Listed || c.Output != 20 {
		t.Errorf("claude listed: %+v", c)
	}
	if c := CostOf(clients.ClaudeCode, "claude-new-9", u); c.Basis != Fallback || PriceOf(clients.ClaudeCode, "claude-new-9").Known {
		t.Errorf("claude fallback: %+v", c)
	}
	want := map[clients.ID]Basis{clients.Codex: Unpriced, clients.GeminiCLI: Unpriced, clients.QwenCode: Unpriced,
		clients.CopilotCLI: Recorded, clients.OpenCode: Recorded}
	// A Claude model id under another client has no price there: only a client's own table counts.
	for _, client := range clients.All() {
		if client == clients.ClaudeCode {
			continue
		}
		c := CostOf(client, "claude-opus-5-5", u)
		if c.Basis != want[client] || c != (Cost{Basis: want[client]}) || PriceOf(client, "gpt-5").Known {
			t.Errorf("%s: %+v, want zero cost marked %d", client, c, want[client])
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("an unlisted client was priced")
		}
	}()
	PriceOf(clients.ID("cursor"), "gpt-5")
}

func TestRecordedCost(t *testing.T) {
	if c, err := RecordedCost(clients.OpenCode, 0.25); err != nil || c.Output != 0.25 || c.Basis != Recorded {
		t.Errorf("opencode: %+v %v", c, err)
	}
	for _, bad := range []struct {
		client clients.ID
		usd    float64
	}{{clients.Codex, 1}, {clients.ClaudeCode, 1}, {clients.CopilotCLI, -1}, {clients.CopilotCLI, math.NaN()},
		{clients.CopilotCLI, math.Inf(1)}} {
		if _, err := RecordedCost(bad.client, bad.usd); err == nil {
			t.Errorf("%s %v accepted", bad.client, bad.usd)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestClientModelPrices(t *testing.T) {
	m := int64(1e6)
	for _, c := range []struct {
		client          clients.ID
		model           string
		u               Usage
		in, cw, cr, out float64
	}{
		{clients.Codex, "gpt-5.5", Usage{Input: 100000, CacheRead: 100000, Output: m}, 0.5, 0, 0.05, 30},
		{clients.Codex, "gpt-5.6-terra", Usage{Input: 100000, Output: m}, 0.2, 0, 0, 12},
		{clients.Codex, "gpt-6-astra", Usage{Input: 100000, CW5: 100000, CacheRead: 72000}, 1, 1.25, 0.072, 0},
		{clients.Codex, "gpt-6.1-sol", Usage{Input: 100000, CacheRead: 100000, Output: m}, 0.2, 0, 0.01, 10},
		{clients.Codex, "gpt-6-sol", Usage{Input: 100000, CacheRead: 100000, Output: m}, 0.2, 0, 0.02, 10},
		{clients.Codex, "gpt-6.1-terra", Usage{Input: 100000, Output: m}, 0.1, 0, 0, 5},
		{clients.Codex, "gpt-6-luna", Usage{Input: 100000, Output: m}, 0.01, 0, 0, 0.5},
		{clients.Codex, "gpt-5.5-2026-04-23", Usage{Input: 100000}, 0.5, 0, 0, 0},
		{clients.GeminiCLI, "gemini-3.1-pro-preview", Usage{Input: 100000, CacheRead: 100000, Output: m}, 0.2, 0, 0.02, 12},
		{clients.GeminiCLI, "gemini-3.1-pro-preview", Usage{Input: 100000, CacheRead: 100001, Output: m}, 0.4, 0, 0.0400004,
			18},
		{clients.GeminiCLI, "gemini-2.5-pro", Usage{Input: 200000, Output: m}, 0.25, 0, 0, 10},
		{clients.GeminiCLI, "gemini-2.5-pro", Usage{Input: 200001, Output: m}, 0.5000025, 0, 0, 15},
		{clients.GeminiCLI, "gemini-3-flash-preview", Usage{Input: m, CacheRead: m, Output: m}, 0.5, 0, 0.05, 3},
		{clients.GeminiCLI, "gemini-2.5-flash", Usage{Input: 5 * m, CacheRead: m, Output: m}, 1.5, 0, 0.03, 2.5},
		{clients.QwenCode, "qwen3-coder-flash", Usage{Input: 30000, CacheRead: 2000, Output: m}, 0.009, 0, 0.00012, 1.5},
		{clients.QwenCode, "qwen3-coder-flash", Usage{Input: 30000, CacheRead: 2001, Output: m}, 0.015, 0, 0.0002001,
			2.5},
		{clients.QwenCode, "qwen3-coder-flash-2025-07-28", Usage{Input: 200000, Output: m}, 0.16, 0, 0, 4},
		{clients.QwenCode, "qwen3-coder-flash", Usage{Input: m, Output: m}, 1.6, 0, 0, 9.6},
	} {
		got := CostOf(c.client, c.model, c.u)
		if got.Basis != Listed || !near(got.Input, c.in) || !near(got.CacheWrite, c.cw) || !near(got.CacheRead, c.cr) ||
			!near(got.Output, c.out) || !PriceOf(c.client, c.model).Known {
			t.Errorf("%s %s %+v: %+v", c.client, c.model, c.u, got)
		}
	}
}

func TestClientCallsWithoutAListedRateStayUnpriced(t *testing.T) {
	for _, c := range []struct {
		client clients.ID
		model  string
		u      Usage
	}{
		{clients.Codex, "gpt-5.5", Usage{Input: 272001}},
		{clients.Codex, "gpt-6.1-sol", Usage{Input: 200000, CacheRead: 72001}},
		{clients.Codex, "gpt-5.5", Usage{Input: 10, CW5: 10}},
		{clients.Codex, "gpt-6-astra", Usage{Input: 10, CW1h: 10}},
		{clients.Codex, "gpt-5.6-sol", Usage{Input: 10}},
		{clients.Codex, "gpt-5.5-pro", Usage{Input: 10}},
		{clients.Codex, "gpt-5.5-mini", Usage{Input: 10}},
		{clients.Codex, "gpt-5.5-20260423", Usage{Input: 10}},
		{clients.Codex, "gpt-5.5-2026-4-23", Usage{Input: 10}},
		{clients.Codex, "gemini-2.5-pro", Usage{Input: 10}},
		{clients.GeminiCLI, "gemini-3-pro-preview", Usage{Input: 10}},
		{clients.GeminiCLI, "gemini-2.5-pro-2025-06-17", Usage{Input: 10}},
		{clients.GeminiCLI, "gemini-2.5-flash", Usage{Input: 10, CW5: 10}},
		{clients.QwenCode, "qwen3-coder-plus", Usage{Input: 10}},
		{clients.QwenCode, "qwen3-coder-flash-2026-01-01", Usage{Input: 10}},
		{clients.QwenCode, "qwen3-coder-flash", Usage{Input: 1000001}},
	} {
		if got := CostOf(c.client, c.model, c.u); got != (Cost{Basis: Unpriced}) {
			t.Errorf("%s %s %+v priced: %+v", c.client, c.model, c.u, got)
		}
	}
}
