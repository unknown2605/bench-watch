package pricing

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"benchwatch/server/internal/clients"
)

func tables(client clients.ID) string {
	if client == clients.ClaudeCode {
		return fmt.Sprintf("%v %v %v %v", prices, longContexts, Write5m, Write1h)
	}
	return fmt.Sprintf("%v %v", clientBasis[client], clientPrices[client])
}

// A change to a client's rates must come with a new version, or kept days stay at the old prices.
func TestPriceVersionsFollowTheTables(t *testing.T) {
	want := map[clients.ID]struct {
		version int
		sum     string
	}{
		clients.ClaudeCode: {1, "bb49544cae99"},
		clients.Codex:      {2, "cf5fa83b3913"},
		clients.GeminiCLI:  {2, "f4aed9d8f0e7"},
		clients.QwenCode:   {2, "add35bab7143"},
		clients.CopilotCLI: {1, "c52268d1c09b"},
		clients.OpenCode:   {1, "c52268d1c09b"},
	}
	for _, client := range clients.All() {
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(tables(client))))[:12]
		if w, ok := want[client]; !ok || got != w.sum || Version(client) != w.version {
			t.Errorf("%s: tables %s version %d; bump its version when its rates change, then pin the new sum", client,
				got, Version(client))
		}
	}
}

func TestRepriceIsExactOnlyWhenTheSumsFixThePrice(t *testing.T) {
	gpt := Stored{Calls: 2, Input: 100000, CacheRead: 50000, Output: 20000}
	c, exact := Reprice(clients.Codex, "gpt-6.1-sol", gpt)
	if !exact || c.Basis != Listed || !near(c.Input+c.CacheRead+c.Output, 0.2+0.005+0.2) {
		t.Errorf("two calls within the first tier: %+v exact %v", c, exact)
	}
	if _, exact := Reprice(clients.Codex, "gpt-6.1-sol", Stored{Calls: 2, Input: 300000, Output: 1}); exact {
		t.Error("two calls over the tier bound were priced, though one of them may be over it")
	}
	if c, exact := Reprice(clients.Codex, "gpt-6.1-sol", Stored{Calls: 1, Input: 300000}); !exact || c.Basis != Unpriced {
		t.Errorf("one call over the bound: %+v exact %v, want exactly unpriced", c, exact)
	}
	if c, exact := Reprice(clients.GeminiCLI, "gemini-3.1-pro-preview", Stored{Calls: 1, Input: 250000}); !exact ||
		!near(c.Input, 1) {
		t.Errorf("one gemini call in the upper tier: %+v exact %v", c, exact)
	}
	if c, exact := Reprice(clients.GeminiCLI, "gemini-2.5-flash", Stored{Calls: 9, Input: 5e6}); !exact ||
		!near(c.Input, 1.5) {
		t.Errorf("a single open tier: %+v exact %v", c, exact)
	}
	if c, exact := Reprice(clients.Codex, "gpt-9", gpt); !exact || c != (Cost{Basis: Unpriced}) {
		t.Errorf("unlisted model: %+v exact %v", c, exact)
	}
	if _, exact := Reprice(clients.Codex, "gpt-6.1-sol", Stored{Calls: 1, Input: 10, CacheWrite: 10}); exact {
		t.Error("a cache write of unknown retention was priced")
	}
	if _, exact := Reprice(clients.OpenCode, "gpt-6.1-sol", gpt); exact {
		t.Error("a recorded cost was repriced")
	}
	if _, exact := Reprice(clients.ClaudeCode, "claude-haiku-5-5", Stored{Calls: 2, Input: 150000}); exact {
		t.Error("two Haiku calls over the long-context bound were priced")
	}
	if c, exact := Reprice(clients.ClaudeCode, "claude-opus-5-5", Stored{Calls: 3, Input: 1e6}); !exact || c.Input != 4 {
		t.Errorf("claude without cache writes: %+v exact %v", c, exact)
	}
}
