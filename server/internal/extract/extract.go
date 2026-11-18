// Package extract turns one parsed transcript record into numeric events; no text leaves it.
package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"

	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/pricing"
)

const (
	Call     = 0
	Result   = 1
	Boundary = 2

	charsPerToken      = 4
	toolReferenceToken = 20
	// MinRepeatTokens is the smallest result that can count as a repeat. Fixed acknowledgements ("ok", a file or task
	// list was updated) say the same thing after different work; the longest known is about 40 tokens.
	MinRepeatTokens = 64
	// Far above any real call (largest seen is under 1M); larger values are corrupt and would overflow the totals.
	maxTokens = 1e9
	// Date.parse accepts years that a local day cannot print as YYYY-MM-DD, the form the guard's --since needs.
	MinTs = 946684800
	MaxTs = 4102444800
)

// Event is one shard item. Text is the model label for a call, the result label for a result and the trigger for a
// boundary; N holds input, cw5, cw1h, cacheRead, output for a call and the token count in N[0] otherwise. Repeat
// marks a result whose content the same transcript already returned for the same label. USD is the cost a client
// recorded for a call, set only when HasUSD; a recorded 0 is a real price.
type Event struct {
	Kind   int
	Ts     int64
	ID     string
	Text   string
	N      [5]int64
	Error  string
	Repeat bool
	USD    float64
	HasUSD bool
}

type State struct {
	Pending map[string]string
	Calls   map[string]int
	LastTs  int64
	Events  []Event
	// Repeat, when set, is asked once per stored result of MinRepeatTokens or more that is not an error, in
	// transcript order.
	Repeat func(label string, content any) bool
	// MaxItems, when positive, is where Events plus Pending stop growing, inside a line too.
	MaxItems int
}

// ErrItemsFull says a line held more items than MaxItems leaves room for; the items before the limit are kept.
var ErrItemsFull = errors.New("EITEMSFULL")

func (s *State) full() bool { return s.MaxItems > 0 && len(s.Events)+len(s.Pending) >= s.MaxItems }

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func HashID(id string) string { return Hash([]byte(id))[:16] }

func HashKey(data []byte) string { return Hash(data)[:32] }

// RoundJS is Math.round: floor(x + 0.5) would round 0.49999999999999994 up.
func RoundJS(x float64) float64 {
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return r
}

func num(v any) int64 {
	f, ok := v.(float64)
	if !ok || math.IsInf(f, 0) || f != f || !(f > 0 && f <= maxTokens) {
		return 0
	}
	return int64(RoundJS(f))
}

func clampTs(sec int64) int64 { return min(MaxTs, max(MinTs, sec)) }

func textTokens(chars int) int64 { return int64((chars + charsPerToken - 1) / charsPerToken) }

type measured struct {
	tokens int64
	image  bool
	sample string
}

func measure(content any) measured {
	if text, ok := content.(string); ok {
		return measured{tokens: textTokens(labels.UTF16Len(text)), sample: text}
	}
	blocks, ok := content.([]any)
	if !ok {
		return measured{}
	}
	var m measured
	chars := 0
	for _, block := range blocks {
		switch Get(block, "type") {
		case "text":
			if text, ok := Get(block, "text").(string); ok {
				chars += labels.UTF16Len(text)
				if m.sample == "" {
					m.sample = text
				}
			}
		case "image":
			m.image = true
			m.tokens += pricing.ImageTokens(Get(Get(block, "source"), "data"))
		case "document":
			m.tokens += pricing.DocumentTokens(Get(Get(block, "source"), "data"))
		case "tool_reference":
			m.tokens += toolReferenceToken
		}
	}
	m.tokens += textTokens(chars)
	return m
}

func callUsage(usage any) [5]int64 {
	written := num(Get(usage, "cache_creation_input_tokens"))
	cw1h := min(num(Get(Get(usage, "cache_creation"), "ephemeral_1h_input_tokens")), written)
	return [5]int64{
		num(Get(usage, "input_tokens")), written - cw1h, cw1h,
		num(Get(usage, "cache_read_input_tokens")), num(Get(usage, "output_tokens")),
	}
}

// ApplyRecord follows the retired Node lib/extract.mjs (4ab56da).
// An error is the throw that makes the scanner count the line as skipped.
func ApplyRecord(record any, state *State, fallbackTs int64) error {
	switch record.(type) {
	case map[string]any, []any:
	default:
		return nil
	}
	parsedMs, finite, err := DateParse(Get(record, "timestamp"))
	if err != nil {
		return err
	}
	var ts int64
	if finite {
		ts = clampTs(int64(math.Floor(parsedMs / 1000)))
		state.LastTs = ts
	} else if state.LastTs != 0 {
		ts = clampTs(state.LastTs)
	} else {
		ts = clampTs(fallbackTs)
	}
	message := Get(record, "message")
	content := Get(message, "content")

	switch Get(record, "type") {
	case "assistant":
		if blocks, ok := content.([]any); ok {
			for _, block := range blocks {
				if id, ok := Get(block, "id").(string); ok && Get(block, "type") == "tool_use" {
					if state.full() {
						return ErrItemsFull
					}
					state.Pending[HashID(id)] = labels.ResultLabel(Get(block, "name"), Get(block, "input"))
				}
			}
		}
		usage := Get(message, "usage")
		rawID, isString := Get(message, "id").(string)
		if !Truthy(usage) || !isString || Get(message, "model") == "<synthetic>" {
			return nil
		}
		u := callUsage(usage)
		id := HashID(rawID)
		if at, seen := state.Calls[id]; seen {
			for i := range u {
				state.Events[at].N[i] = max(state.Events[at].N[i], u[i])
			}
			return nil
		}
		if state.full() {
			return ErrItemsFull
		}
		state.Calls[id] = len(state.Events)
		state.Events = append(state.Events, Event{Kind: Call, Ts: ts, ID: id, Text: labels.ModelLabel(Get(message, "model")), N: u})
	case "user":
		blocks, ok := content.([]any)
		if !ok {
			return nil
		}
		for _, block := range blocks {
			rawUse, ok := Get(block, "tool_use_id").(string)
			if Get(block, "type") != "tool_result" || !ok {
				continue
			}
			if state.full() {
				return ErrItemsFull
			}
			useID := HashID(rawUse)
			label, known := state.Pending[useID]
			if !known {
				label = "other"
			}
			delete(state.Pending, useID)
			content := Get(block, "content")
			m := measure(content)
			if m.image && strings.HasPrefix(label, "Read:") {
				label = "Read: image"
			}
			errorText, failed := "", Get(block, "is_error") == true
			if failed {
				errorText = labels.ErrorClass(Truthy(Get(record, "toolDenialKind")), m.sample)
			}
			repeat := state.Repeat != nil && !failed && m.tokens >= MinRepeatTokens && state.Repeat(label, content)
			state.Events = append(state.Events, Event{Kind: Result, Ts: ts, ID: useID, Text: label,
				N: [5]int64{m.tokens}, Error: errorText, Repeat: repeat})
		}
	case "system":
		if Get(record, "subtype") != "compact_boundary" {
			return nil
		}
		meta := Get(record, "compactMetadata")
		trigger := "auto"
		if Get(meta, "trigger") == "manual" {
			trigger = "manual"
		}
		if state.full() {
			return ErrItemsFull
		}
		id := strconv.FormatInt(ts, 10)
		if uuid, ok := Get(record, "uuid").(string); ok {
			id = HashID(uuid)
		}
		state.Events = append(state.Events, Event{Kind: Boundary, Ts: ts, ID: id, Text: trigger, N: [5]int64{num(Get(meta, "preTokens"))}})
	}
	return nil
}
