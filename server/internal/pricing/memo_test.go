package pricing

import (
	"fmt"
	"reflect"
	"testing"

	"benchwatch/server/internal/clients"
)

func TestPriceOfMemoGivesTheComputedPrice(t *testing.T) {
	models := []string{"claude-opus-5-5", "claude-haiku-5-5", "claude-haiku-5-5-20261001", "claude-sonnet-5[1m]",
		"claude-mystery-9", "", "unknown", "claude-haiku-5-5[x] "}
	for i := range memoMax + 10 {
		models = append(models, fmt.Sprintf("claude-x-%d", i))
	}
	for range 2 {
		for _, m := range models {
			if got, want := PriceOf(clients.ClaudeCode, m), priceOf(m); !reflect.DeepEqual(got, want) {
				t.Fatalf("PriceOf(%q) = %+v, want %+v", m, got, want)
			}
		}
	}
	memo.Lock()
	n := len(memo.prices)
	memo.Unlock()
	if n > memoMax {
		t.Fatalf("memo holds %d prices, cap %d", n, memoMax)
	}
}
