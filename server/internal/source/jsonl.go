package source

import (
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"benchwatch/server/internal/extract"
)

var hexHead = regexp.MustCompile(`^[0-9a-f]{8}`)

// SessionLabel is the first 8 hex characters of a session id, or of its hash when the id does not start with 8.
func SessionLabel(id string) string {
	if lower := strings.ToLower(id); hexHead.MatchString(lower) {
		return lower[:8]
	}
	return extract.HashID(id)[:8]
}

// Dashed is Claude Code's project folder name for a path: every UTF-16 unit outside [A-Za-z0-9] becomes '-'.
func Dashed(path string) string {
	var b strings.Builder
	for _, c := range path {
		switch {
		case c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'):
			b.WriteRune(c)
		case c > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// Lines calls fn with every complete line of in, or with nil for a line longer than maxLine. It returns the bytes
// after the last newline (nil when they passed maxLine) and the bytes consumed up to and including that newline.
func Lines(in io.Reader, maxLine int64, tick func() error, fn func(line []byte)) (tail []byte, consumed int64,
	err error) {
	var held []byte
	var lineBytes int64
	over := false
	chunk := make([]byte, 1024*1024)
	for {
		got, rerr := in.Read(chunk)
		if got > 0 && tick != nil {
			if err := tick(); err != nil {
				return nil, consumed, err
			}
		}
		data := chunk[:got]
		at0 := 0
		for {
			at := indexByte(data[at0:], '\n')
			if at < 0 {
				break
			}
			piece := data[at0 : at0+at]
			lineBytes += int64(len(piece))
			switch {
			case over || lineBytes > maxLine:
				fn(nil)
			case len(held) > 0:
				fn(append(held, piece...))
			default:
				fn(piece)
			}
			consumed += lineBytes + 1
			held, lineBytes, over = held[:0], 0, false
			at0 += at + 1
		}
		if at0 < got {
			lineBytes += int64(got - at0)
			if lineBytes > maxLine {
				held, over = nil, true
			} else {
				held = append(held, data[at0:]...)
			}
		}
		if rerr == io.EOF {
			if over {
				return nil, consumed, nil
			}
			return held, consumed, nil
		}
		if rerr != nil {
			return held, consumed, rerr
		}
	}
}

// MaxRecordedUSD bounds one call's recorded cost; the cost columns are numeric(18,6), so a larger value would fail
// the whole insert.
const MaxRecordedUSD = 1e6

// FormatFloor is the size above which a unit whose records yield no usage at all is taken for an unknown format.
const FormatFloor = 64 << 10

// ErrUnrecognised marks a unit whose lines matched no usage shape the reader knows.
var ErrUnrecognised = errors.New("no usage record recognised")

// PendingKnown is the Pending key that marks, across resumed reads, a unit that held a record of a known type.
const PendingKnown = "known"

// MarkKnown records that the unit held a record whose type its reader knows.
func (c *Cursor) MarkKnown() { c.setKnown("1") }

// PendingWithheld is the Pending key that marks a unit holding a usage record its reader knows but cannot count.
const PendingWithheld = "withheld"

// MarkWithheld records a known usage record skipped on purpose; a reader sets the key to "0" on malformed usage.
func (c *Cursor) MarkWithheld() {
	if c.Pending == nil {
		c.Pending = map[string]string{}
	}
	if c.Pending[PendingWithheld] != "0" {
		c.Pending[PendingWithheld] = "1"
	}
}

func (c *Cursor) setKnown(v string) {
	if c.Pending == nil {
		c.Pending = map[string]string{}
	}
	c.Pending[PendingKnown] = v
}

// Unrecognised fails a read with lines but no usage so it keeps its previous shard: at any size when no record type
// was known, above FormatFloor otherwise. A header alone or usage withheld on purpose never fails.
func Unrecognised(unit Unit, from Cursor, cur *Cursor, unknown bool) error {
	// from is the resumed cursor (zero for a unit read whole); one with lines but no key is from an older build.
	_, recorded := from.Pending[PendingKnown]
	legacy := from.Lines > 0 && !recorded
	if _, ok := cur.Pending[PendingKnown]; !ok && !legacy {
		cur.setKnown("0")
	}
	if len(cur.Events) > 0 || cur.Lines == 0 || cur.Pending[PendingWithheld] == "1" {
		return nil
	}
	if unit.Size >= FormatFloor {
		return fmt.Errorf("%w: %d lines", ErrUnrecognised, cur.Lines)
	}
	if unknown && !legacy && cur.Pending[PendingKnown] != "1" {
		return fmt.Errorf("%w: %d lines, no known record type", ErrUnrecognised, cur.Lines)
	}
	return nil
}

// Finite says whether v is a JSON number that is neither NaN nor infinite.
func Finite(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsInf(f, 0) && f == f
}

// Tokens reads one record's token fields; Found says whether any of them held a finite number.
type Tokens struct{ Found bool }

func (t *Tokens) Count(v any) int64 {
	t.Found = t.Found || Finite(v)
	return Count(v)
}

// Count is a token count field: a finite number in (0, 1e9], else 0.
func Count(v any) int64 {
	f, ok := v.(float64)
	if !ok || math.IsInf(f, 0) || f != f || !(f > 0 && f <= 1e9) {
		return 0
	}
	return int64(extract.RoundJS(f))
}

// Ts is a record's timestamp in seconds, clamped; ok is false when the value is no date.
func Ts(v any) (int64, bool) {
	ms, finite, err := extract.DateParse(v)
	if err != nil || !finite {
		return 0, false
	}
	return min(extract.MaxTs, max(extract.MinTs, int64(math.Floor(ms/1000)))), true
}

// BadTs says whether a record carries a timestamp field that is no date, so the read falls back to another time.
func BadTs(v any) bool {
	if v == nil {
		return false
	}
	_, ok := Ts(v)
	return !ok
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// Calls is one read's call events with an index by ID: a record that repeats an ID raises that event's counts.
type Calls struct {
	Cur Cursor
	// Unknown says this read met a line that is neither a header nor a record type the reader knows.
	Unknown  bool
	index    map[string]int
	maxItems int
}

// NewCalls copies from, so the stored cursor is never changed by a failed read.
func NewCalls(from Cursor, maxItems int) *Calls {
	c := &Calls{Cur: from, index: map[string]int{}, maxItems: maxItems}
	c.Cur.Pending = make(map[string]string, len(from.Pending))
	for k, v := range from.Pending {
		c.Cur.Pending[k] = v
	}
	c.Cur.Events = append([]extract.Event(nil), from.Events...)
	for i, e := range c.Cur.Events {
		c.index[e.ID] = i
	}
	return c
}

// Add files one call under id; a repeat only raises counts, as the scanner sees no other change to a stored event.
// A record whose token fields held no number (t.Found false) is a changed format, not a free call: it is skipped.
func (c *Calls) Add(id string, ts int64, model string, n [5]int64, t Tokens) {
	c.add(id, ts, model, n, t, 0, false)
}

// AddRecorded is Add with the USD the client recorded for the call; a repeat keeps the larger cost, like the counts.
func (c *Calls) AddRecorded(id string, ts int64, model string, n [5]int64, t Tokens, usd float64) {
	c.add(id, ts, model, n, t, usd, true)
}

func (c *Calls) add(id string, ts int64, model string, n [5]int64, t Tokens, usd float64, hasUSD bool) {
	if !t.Found {
		c.Cur.Skipped++
		return
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		usd, hasUSD = 0, false
	}
	if hasUSD && usd > MaxRecordedUSD {
		c.Cur.Skipped++
		return
	}
	if i, ok := c.index[id]; ok {
		e := &c.Cur.Events[i]
		for k := range e.N {
			e.N[k] = max(e.N[k], n[k])
		}
		if hasUSD {
			e.USD, e.HasUSD = max(e.USD, usd), true
		}
		return
	}
	if c.maxItems > 0 && len(c.Cur.Events)+len(c.Cur.Pending) >= c.maxItems {
		c.Cur.Skipped++
		return
	}
	c.index[id] = len(c.Cur.Events)
	c.Cur.Events = append(c.Cur.Events, extract.Event{Kind: extract.Call, Ts: ts, ID: id, Text: model, N: n, USD: usd,
		HasUSD: hasUSD})
}
