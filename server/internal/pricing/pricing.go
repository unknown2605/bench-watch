// Package pricing holds API list prices and the image and PDF token estimates.
package pricing

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sync"

	"benchwatch/server/internal/clients"
)

type rates struct{ input, output, cacheRead float64 }

// USD per million tokens, from the claude-api skill's model tables (cached 2026-10-06). Order is the price table's.
var prices = []struct {
	model string
	rates
}{
	{"claude-fable-5-1", rates{10, 50, 0.25}},
	{"claude-fable-5", rates{10, 50, 1}},
	{"claude-opus-5-5", rates{4, 20, 0.2}},
	{"claude-opus-5", rates{5, 25, 0.5}},
	{"claude-opus-4-8", rates{5, 25, 0.5}},
	{"claude-opus-4-7", rates{5, 25, 0.5}},
	{"claude-opus-4-6", rates{5, 25, 0.5}},
	{"claude-sonnet-5-5", rates{2, 10, 0.2}},
	{"claude-sonnet-5", rates{2, 10, 0.2}},
	{"claude-sonnet-4-6", rates{3, 15, 0.3}},
	{"claude-haiku-5-5", rates{0.1, 0.5, 0.01}},
	{"claude-haiku-4-5", rates{1, 5, 0.1}},
}

type longContext struct {
	above int64
	rates
}

// Docs price Haiku 5.5 higher above 100k prompt tokens; the cache-read rate assumes 0.1× input, as they list none.
var longContexts = map[string]longContext{"claude-haiku-5-5": {100000, rates{0.5, 2.5, 0.05}}}

const (
	fallback = "claude-opus-5-5"
	Write5m  = 1.25
	Write1h  = 2
)

var (
	dateSuffix = regexp.MustCompile(`-[0-9]{8}$`)
	// JavaScript's /\[.*\]$/: its dot refuses all four line terminators, RE2's only \n.
	tagSuffix = regexp.MustCompile(`\[[^\n\r\x{2028}\x{2029}]*\]$`)
)

// Basis says where a price comes from; the zero value is never returned.
type Basis uint8

const (
	// Listed is the model's list price.
	Listed Basis = iota + 1
	// Fallback prices a Claude model missing from the table as the fallback model; it is counted as unpriced.
	Fallback
	// Unpriced is a call with no listed rate (model, prompt size or cache write): the cost is zero and must be shown
	// as unknown, not as free.
	Unpriced
	// Recorded is a client that stores its own cost per call; take it through RecordedCost.
	Recorded
)

var clientBasis = map[clients.ID]Basis{
	clients.Codex: Listed, clients.GeminiCLI: Listed, clients.QwenCode: Listed,
	clients.CopilotCLI: Recorded, clients.OpenCode: Recorded,
}

// tier prices a call whose prompt (input + cache write + cache read) is at most upTo tokens; the whole call takes the
// tier's rates. cacheWrite is per 1M tokens written at the shortest listed retention, 0 when none is listed.
type tier struct {
	upTo int64
	rates
	cacheWrite float64
}

const noLimit = math.MaxInt64

// OpenAI's Codex models, Standard, short context only: https://developers.openai.com/api/docs/pricing, checked
// 2026-11-16. The page lists no long-context (>272K input) rate for them, so such a call stays unpriced.
var openAIPrices = map[string][]tier{
	"gpt-6-astra":   {{272000, rates{10, 50, 1}, 12.5}},
	"gpt-6.1-sol":   {{272000, rates{2, 10, 0.1}, 2.5}},
	"gpt-6-sol":     {{272000, rates{2, 10, 0.2}, 2.5}},
	"gpt-6.1-terra": {{272000, rates{1, 5, 0.05}, 1.25}},
	"gpt-6-luna":    {{272000, rates{0.1, 0.5, 0.01}, 0.125}},
	"gpt-5.6-terra": {{272000, rates{2, 12, 0.2}, 2.5}},
	// The vendor page's toggle reads "≤272K input tokens", which governs over its row label "<272K".
	"gpt-5.5": {{272000, rates{5, 30, 0.5}, 0}},
}

// Gemini paid tier, text input, output includes thinking: https://ai.google.dev/gemini-api/docs/pricing, checked
// 2026-11-16. Above 200k prompt tokens the whole call bills at the higher rate.
var geminiPrices = map[string][]tier{
	"gemini-3.1-pro-preview":             {{200000, rates{2, 12, 0.2}, 0}, {noLimit, rates{4, 18, 0.4}, 0}},
	"gemini-3.1-pro-preview-customtools": {{200000, rates{2, 12, 0.2}, 0}, {noLimit, rates{4, 18, 0.4}, 0}},
	"gemini-2.5-pro":                     {{200000, rates{1.25, 10, 0.125}, 0}, {noLimit, rates{2.5, 15, 0.25}, 0}},
	"gemini-3-flash-preview":             {{noLimit, rates{0.5, 3, 0.05}, 0}},
	"gemini-2.5-flash":                   {{noLimit, rates{0.3, 2.5, 0.03}, 0}},
}

// Alibaba Model Studio, Singapore: https://www.alibabacloud.com/help/en/model-studio/model-pricing, checked 2026-11-16.
// Cache read is 20% of the tier's input (implicit cache, model-studio/qwen-coder); above 1M input is unpriced.
var qwenCoderFlash = []tier{
	{32000, rates{0.3, 1.5, 0.06}, 0}, {128000, rates{0.5, 2.5, 0.1}, 0},
	{256000, rates{0.8, 4, 0.16}, 0}, {1000000, rates{1.6, 9.6, 0.32}, 0},
}

var qwenPrices = map[string][]tier{"qwen3-coder-flash": qwenCoderFlash, "qwen3-coder-flash-2025-07-28": qwenCoderFlash}

var clientPrices = map[clients.ID]map[string][]tier{
	clients.Codex: openAIPrices, clients.GeminiCLI: geminiPrices, clients.QwenCode: qwenPrices,
}

// isoDate answers whether s is YYYY-MM-DD in digits, the suffix of OpenAI's dated snapshots.
func isoDate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range []byte(s) {
		if i != 4 && i != 7 && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// clientTiers finds a model of a client with its own price table; OpenAI's dated snapshots take their base's price.
func clientTiers(client clients.ID, model string) ([]tier, bool) {
	table := clientPrices[client]
	if t, ok := table[model]; ok {
		return t, true
	}
	if n := len(model); client == clients.Codex && n > 11 && model[n-11] == '-' && isoDate(model[n-10:]) {
		t, ok := table[model[:n-11]]
		return t, ok
	}
	return nil, false
}

type Price struct {
	rates
	Known bool
	Basis Basis
	long  *longContext
	tiers []tier
}

func lookup(model string) (rates, bool) {
	for _, p := range prices {
		if p.model == model {
			return p.rates, true
		}
	}
	return rates{}, false
}

// Prices of the models seen so far; a bounded memo, because the replay asks once per call and the regexps dominate.
var memo = struct {
	sync.Mutex
	prices map[string]Price
}{prices: map[string]Price{}}

const memoMax = 1024

// PriceOf panics on a client outside clients.All, so a new client cannot be priced by accident.
func PriceOf(client clients.ID, model string) Price {
	if client != clients.ClaudeCode {
		b, ok := clientBasis[client]
		if !ok {
			panic(fmt.Sprintf("pricing: no price basis for client %q", client))
		}
		if b == Recorded {
			return Price{Basis: b}
		}
		if t, ok := clientTiers(client, model); ok {
			return Price{Known: true, Basis: Listed, tiers: t}
		}
		return Price{Basis: Unpriced}
	}
	memo.Lock()
	p, ok := memo.prices[model]
	memo.Unlock()
	if ok {
		return p
	}
	p = priceOf(model)
	memo.Lock()
	if len(memo.prices) < memoMax {
		memo.prices[model] = p
	}
	memo.Unlock()
	return p
}

func priceOf(model string) Price {
	base := tagSuffix.ReplaceAllLiteralString(dateSuffix.ReplaceAllLiteralString(model, ""), "")
	r, known := lookup(base)
	if !known {
		r, _ = lookup(fallback)
	}
	p := Price{rates: r, Known: known, Basis: Listed}
	if !known {
		p.Basis = Fallback
	}
	if lc, ok := longContexts[base]; known && ok {
		p.long = &lc
	}
	return p
}

type Usage struct{ Input, CW5, CW1h, CacheRead, Output int64 }

type Cost struct {
	Input, CacheWrite, CacheRead, Output float64
	Basis                                Basis
}

// CostOf mirrors the retired Node lib/pricing.mjs (4ab56da) bit for bit for Claude Code:
// each float64() conversion stops arm64 from fusing a*b+c into one FMA. A call with no listed rate gets a zero cost
// marked Unpriced; a Recorded client's cost comes from RecordedCost.
func CostOf(client clients.ID, model string, u Usage) Cost {
	p := PriceOf(client, model)
	if p.Basis == Unpriced || p.Basis == Recorded {
		return Cost{Basis: p.Basis}
	}
	if p.tiers != nil {
		return tieredCost(p.tiers, u)
	}
	r := p.rates
	if p.long != nil && u.Input+u.CW5+u.CW1h+u.CacheRead > p.long.above {
		r = p.long.rates
	}
	written := float64(float64(u.CW5)*Write5m) + float64(float64(u.CW1h)*Write1h)
	return Cost{
		Input:      float64(float64(u.Input)*r.input) / 1e6,
		CacheWrite: float64(written*r.input) / 1e6,
		CacheRead:  float64(float64(u.CacheRead)*r.cacheRead) / 1e6,
		Output:     float64(float64(u.Output)*r.output) / 1e6,
		Basis:      p.Basis,
	}
}

func tieredCost(tiers []tier, u Usage) Cost {
	prompt := u.Input + u.CW5 + u.CW1h + u.CacheRead
	i := slices.IndexFunc(tiers, func(t tier) bool { return prompt <= t.upTo })
	if i < 0 || u.CW1h > 0 || (u.CW5 > 0 && tiers[i].cacheWrite == 0) {
		return Cost{Basis: Unpriced}
	}
	r := tiers[i]
	return Cost{
		Input:      float64(float64(u.Input)*r.input) / 1e6,
		CacheWrite: float64(float64(u.CW5)*r.cacheWrite) / 1e6,
		CacheRead:  float64(float64(u.CacheRead)*r.cacheRead) / 1e6,
		Output:     float64(float64(u.Output)*r.output) / 1e6,
		Basis:      Listed,
	}
}

// versions number each client's price basis. Bump a client's number with any change to its rates: stored days that
// no rebuild reaches are then repriced once (TestPriceVersionsFollowTheTables pins each table to its number).
var versions = map[clients.ID]int{
	clients.ClaudeCode: 1, clients.Codex: 2, clients.GeminiCLI: 2, clients.QwenCode: 2,
	clients.CopilotCLI: 1, clients.OpenCode: 1,
}

// Version is the client's price basis; 1, the basis before Codex, Gemini and Qwen were priced, stands for a stored
// day that recorded none.
func Version(client clients.ID) int {
	v, ok := versions[client]
	if !ok {
		panic(fmt.Sprintf("pricing: no price version for client %q", client))
	}
	return v
}

// Stored is one stored row: token sums over Calls calls, with 5-minute and 1-hour cache writes added together.
type Stored struct{ Calls, Input, CacheWrite, CacheRead, Output int64 }

// Reprice prices a stored row at today's rates. exact is false when the sums cannot give the per-call price: a
// recorded cost, cache writes of unknown retention, or several calls that may fall in different prompt tiers.
func Reprice(client clients.ID, model string, s Stored) (cost Cost, exact bool) {
	p := PriceOf(client, model)
	switch {
	case p.Basis == Recorded:
		return Cost{Basis: Recorded}, false
	case p.Basis == Unpriced:
		return Cost{Basis: Unpriced}, true
	case s.CacheWrite > 0:
		return Cost{}, false
	}
	u := Usage{Input: s.Input, CacheRead: s.CacheRead, Output: s.Output}
	// Each call's prompt is at most the row's, so a row within the first tier has every call in it.
	prompt := s.Input + s.CacheRead
	if s.Calls > 1 && (p.tiers != nil && prompt > p.tiers[0].upTo || p.long != nil && prompt > p.long.above) {
		return Cost{}, false
	}
	return CostOf(client, model, u), true
}

// RecordsCost answers whether the client stores its own cost per call, which has no split by token kind.
func RecordsCost(client clients.ID) bool { return clientBasis[client] == Recorded }

// RecordedCost takes the USD a Recorded client stored for one call; the client's figure has no token split, so it
// is booked as output cost.
func RecordedCost(client clients.ID, usd float64) (Cost, error) {
	if b, ok := clientBasis[client]; !ok || b != Recorded {
		return Cost{}, fmt.Errorf("pricing: client %q records no cost", client)
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return Cost{}, fmt.Errorf("pricing: recorded cost %v of client %q is not a finite non-negative number", usd, client)
	}
	return Cost{Output: usd, Basis: Recorded}, nil
}

type LongContextRow struct {
	Above     int64   `json:"above"`
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cacheRead"`
}

type PriceRow struct {
	Model       string          `json:"model"`
	Input       float64         `json:"input"`
	Output      float64         `json:"output"`
	CacheRead   float64         `json:"cacheRead"`
	LongContext *LongContextRow `json:"longContext,omitempty"`
}

func PriceTable() []PriceRow {
	rows := make([]PriceRow, 0, len(prices))
	for _, p := range prices {
		row := PriceRow{Model: p.model, Input: p.input, Output: p.output, CacheRead: p.cacheRead}
		if lc, ok := longContexts[p.model]; ok {
			row.LongContext = &LongContextRow{lc.above, lc.input, lc.output, lc.cacheRead}
		}
		rows = append(rows, row)
	}
	return rows
}
