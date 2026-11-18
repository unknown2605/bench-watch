package extract

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"benchwatch/server/internal/labels"
)

// ErrToPrimitive is the TypeError V8 throws when an object's own toString key shadows the callable one.
var ErrToPrimitive = errors.New("cannot convert object to primitive value")

// NumberString is JavaScript's Number.prototype.toString() for a finite or infinite double.
func NumberString(f float64) string {
	switch {
	case f != f:
		return "NaN"
	case f == 0:
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	text := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exponent, _ := strings.Cut(text, "e")
	digits := strings.Replace(mantissa, ".", "", 1)
	e, _ := strconv.Atoi(exponent)
	k, n := len(digits), e+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	expSign := "+"
	if n-1 < 0 {
		expSign = "-"
	}
	exp := strconv.Itoa(int(math.Abs(float64(n - 1))))
	if k == 1 {
		return sign + digits + "e" + expSign + exp
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + exp
}

// ErrStackDepth stands for the RangeError V8 throws when String() nests arrays too deeply.
var ErrStackDepth = errors.New("maximum call stack size exceeded")

// MaxToStringDepth is below the 2822 nested arrays at which Node 24 fails; Go would otherwise overflow near 8M.
// Lines nested from 2001 to about 2800 levels are skipped here and counted by Node (CM-00003 porting risks).
const MaxToStringDepth = 2000

// ToString is JavaScript's String(value) on a JSON.parse result.
func ToString(value any) (string, error) { return toString(value, 0) }

func toString(value any, depth int) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case float64:
		return NumberString(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case nil:
		return "null", nil
	case undefined:
		return "undefined", nil
	case []any:
		if depth >= MaxToStringDepth {
			return "", ErrStackDepth
		}
		parts := make([]string, len(v))
		for i, item := range v {
			if item == nil || item == Undefined {
				continue
			}
			text, err := toString(item, depth+1)
			if err != nil {
				return "", err
			}
			parts[i] = text
		}
		return strings.Join(parts, ","), nil
	case map[string]any:
		if _, own := v["toString"]; own {
			return "", ErrToPrimitive
		}
		return "[object Object]", nil
	}
	return "", ErrToPrimitive
}

const (
	dNone           = math.MaxInt32
	maxSignificant  = 9
	maxTimeMs       = 8.64e15
	maxTimeBeforeMs = 8.64e15 + 8.64e8
	smiMax          = math.MaxInt32
)

const (
	kwInvalid = iota
	kwMonth
	kwZone
	kwTimeSeparator
	kwAmPm
)

const (
	tagKeyword = iota
	tagNumber
	tagSymbol
	tagSpace
	tagEnd
	tagUnknown
	tagInvalid
)

type dateToken struct {
	tag, length, value, kind int
}

func (t dateToken) isNumber() bool              { return t.tag == tagNumber }
func (t dateToken) isFixed(n int) bool          { return t.tag == tagNumber && t.length == n }
func (t dateToken) isSymbol(c int) bool         { return t.tag == tagSymbol && t.value == c }
func (t dateToken) isSign() bool                { return t.isSymbol('+') || t.isSymbol('-') }
func (t dateToken) sign() int                   { return 44 - t.value }
func (t dateToken) isKeywordKind(kind int) bool { return t.tag == tagKeyword && t.kind == kind }
func (t dateToken) isZ() bool                   { return t.isKeywordKind(kwZone) && t.length == 1 && t.value == 0 }

var keywords = [...]struct {
	prefix [3]rune
	kind   int
	value  int
}{
	{[3]rune{'j', 'a', 'n'}, kwMonth, 1}, {[3]rune{'f', 'e', 'b'}, kwMonth, 2}, {[3]rune{'m', 'a', 'r'}, kwMonth, 3},
	{[3]rune{'a', 'p', 'r'}, kwMonth, 4}, {[3]rune{'m', 'a', 'y'}, kwMonth, 5}, {[3]rune{'j', 'u', 'n'}, kwMonth, 6},
	{[3]rune{'j', 'u', 'l'}, kwMonth, 7}, {[3]rune{'a', 'u', 'g'}, kwMonth, 8}, {[3]rune{'s', 'e', 'p'}, kwMonth, 9},
	{[3]rune{'o', 'c', 't'}, kwMonth, 10}, {[3]rune{'n', 'o', 'v'}, kwMonth, 11}, {[3]rune{'d', 'e', 'c'}, kwMonth, 12},
	{[3]rune{'a', 'm', 0}, kwAmPm, 0}, {[3]rune{'p', 'm', 0}, kwAmPm, 12},
	{[3]rune{'u', 't', 0}, kwZone, 0}, {[3]rune{'u', 't', 'c'}, kwZone, 0}, {[3]rune{'z', 0, 0}, kwZone, 0},
	{[3]rune{'g', 'm', 't'}, kwZone, 0}, {[3]rune{'c', 'd', 't'}, kwZone, -5}, {[3]rune{'c', 's', 't'}, kwZone, -6},
	{[3]rune{'e', 'd', 't'}, kwZone, -4}, {[3]rune{'e', 's', 't'}, kwZone, -5}, {[3]rune{'m', 'd', 't'}, kwZone, -6},
	{[3]rune{'m', 's', 't'}, kwZone, -7}, {[3]rune{'p', 'd', 't'}, kwZone, -7}, {[3]rune{'p', 's', 't'}, kwZone, -8},
	{[3]rune{'t', 0, 0}, kwTimeSeparator, 0},
}

// dateScanner follows V8's DateStringTokenizer over UTF-16 units; a NUL unit reads as the end, as in V8.
type dateScanner struct {
	units []uint16
	index int
	ch    rune
	next  dateToken
}

func newDateScanner(s string) *dateScanner {
	d := &dateScanner{units: utf16.Encode([]rune(s))}
	d.advance()
	d.next = d.scan()
	return d
}

func (d *dateScanner) advance() {
	if d.index < len(d.units) {
		d.ch = rune(d.units[d.index])
	} else {
		d.ch = 0
	}
	d.index++
}

func (d *dateScanner) isDigit() bool { return d.ch >= '0' && d.ch <= '9' }

// V8's date tokenizer uses WhiteSpace only: line terminators are not spaces there.
func (d *dateScanner) space() bool { return labels.JSSpace(d.ch) && !labels.LineTerminator(d.ch) }

func (d *dateScanner) wordChar() bool { return d.ch >= 'A' && !d.space() }

func (d *dateScanner) scan() dateToken {
	start := d.index
	switch {
	case d.ch == 0:
		return dateToken{tag: tagEnd}
	case d.isDigit():
		n, count := 0, 0
		for d.ch == '0' {
			d.advance()
		}
		for d.isDigit() {
			if count < maxSignificant {
				n = n*10 + int(d.ch-'0')
			}
			count++
			d.advance()
		}
		return dateToken{tag: tagNumber, value: n, length: d.index - start}
	case d.ch == ':' || d.ch == '-' || d.ch == '+' || d.ch == '.' || d.ch == ')':
		c := int(d.ch)
		d.advance()
		return dateToken{tag: tagSymbol, value: c}
	case d.wordChar():
		var prefix [3]rune
		length := 0
		for ; d.wordChar(); d.advance() {
			if length < 3 {
				prefix[length] = d.ch | 0x20
			}
			length++
		}
		for _, k := range keywords {
			if k.prefix == prefix && (length <= 3 || k.kind == kwMonth) {
				return dateToken{tag: tagKeyword, kind: k.kind, value: k.value, length: length}
			}
		}
		return dateToken{tag: tagKeyword, kind: kwInvalid, length: length}
	case d.space():
		d.advance()
		return dateToken{tag: tagSpace, length: d.index - start}
	case d.ch == '(':
		balance := 0
		for {
			if d.ch == ')' {
				balance--
			} else if d.ch == '(' {
				balance++
			}
			d.advance()
			if balance <= 0 || d.ch == 0 {
				break
			}
		}
		return dateToken{tag: tagUnknown}
	}
	d.advance()
	return dateToken{tag: tagUnknown}
}

func (d *dateScanner) peek() dateToken { return d.next }

func (d *dateScanner) take() dateToken {
	t := d.next
	d.next = d.scan()
	return t
}

func (d *dateScanner) skipSymbol(c int) bool {
	if d.next.isSymbol(c) {
		d.next = d.scan()
		return true
	}
	return false
}

func between(x, lo, hi int) bool { return x >= lo && x <= hi }
func isMonth(x int) bool         { return between(x, 1, 12) }
func isDay(x int) bool           { return between(x, 1, 31) }
func isHour(x int) bool          { return between(x, 0, 23) }
func isMinute(x int) bool        { return between(x, 0, 59) }
func isMilli(x int) bool         { return between(x, 0, 999) }

type dayComposer struct {
	comp  [3]int
	index int
	named int
	iso   bool
}

func (c *dayComposer) add(n int) bool {
	if c.index < 3 {
		c.comp[c.index] = n
		c.index++
		return true
	}
	return false
}

func (c *dayComposer) write() (year, month, day int, ok bool) {
	if c.index < 1 {
		return 0, 0, 0, false
	}
	for c.index < 3 {
		c.comp[c.index] = 1
		c.index++
	}
	month, day = dNone, dNone
	switch {
	case c.named == dNone && (c.iso || !isDay(c.comp[0])):
		year, month, day = c.comp[0], c.comp[1], c.comp[2]
	case c.named == dNone:
		month, day, year = c.comp[0], c.comp[1], c.comp[2]
	case !isDay(c.comp[0]):
		month, year, day = c.named, c.comp[0], c.comp[1]
	default:
		month, day, year = c.named, c.comp[0], c.comp[1]
	}
	if !c.iso {
		if between(year, 0, 49) {
			year += 2000
		} else if between(year, 50, 99) {
			year += 1900
		}
	}
	return year, month - 1, day, isMonth(month) && isDay(day)
}

type timeComposer struct {
	comp       [4]int
	index      int
	hourOffset int
}

func (c *timeComposer) empty() bool { return c.index == 0 }

func (c *timeComposer) expecting(n int) bool {
	return (c.index == 1 && isMinute(n)) || (c.index == 2 && isMinute(n)) || (c.index == 3 && isMilli(n))
}

func (c *timeComposer) add(n int) bool {
	if c.index < 4 {
		c.comp[c.index] = n
		c.index++
		return true
	}
	return false
}

func (c *timeComposer) addFinal(n int) bool {
	if !c.add(n) {
		return false
	}
	for c.index < 4 {
		c.comp[c.index] = 0
		c.index++
	}
	return true
}

func (c *timeComposer) write() ([4]int, bool) {
	for c.index < 4 {
		c.comp[c.index] = 0
		c.index++
	}
	h, m, s, ms := c.comp[0], c.comp[1], c.comp[2], c.comp[3]
	if c.hourOffset != dNone {
		if !between(h, 0, 12) {
			return c.comp, false
		}
		h = h%12 + c.hourOffset
	}
	if !isHour(h) || !isMinute(m) || !isMinute(s) || !isMilli(ms) {
		if h != 24 || m != 0 || s != 0 || ms != 0 {
			return c.comp, false
		}
	}
	return [4]int{h, m, s, ms}, true
}

type zoneComposer struct{ sign, hour, minute int }

func (c *zoneComposer) set(hours int) {
	c.sign = 1
	if hours < 0 {
		c.sign = -1
	}
	c.hour, c.minute = hours*c.sign, 0
}

func (c *zoneComposer) setSign(s int) {
	c.sign = 1
	if s < 0 {
		c.sign = -1
	}
}

func (c *zoneComposer) expecting(n int) bool {
	return c.hour != dNone && c.minute == dNone && isMinute(n)
}
func (c *zoneComposer) isUTC() bool { return c.hour == 0 && c.minute == 0 }
func (c *zoneComposer) empty() bool { return c.hour == dNone }

// write returns the offset in seconds, NaN for local time. V8 sums in unsigned 32-bit arithmetic.
func (c *zoneComposer) write() (float64, bool) {
	if c.sign == dNone {
		return math.NaN(), true
	}
	if c.hour == dNone {
		c.hour = 0
	}
	if c.minute == dNone {
		c.minute = 0
	}
	total := uint32(c.hour)*3600 + uint32(c.minute)*60
	if total > smiMax {
		return 0, false
	}
	if c.sign < 0 {
		return -float64(total), true
	}
	return float64(total), true
}

func readMillis(t dateToken) int {
	n, length := t.value, t.length
	switch {
	case length == 1:
		n *= 100
	case length == 2:
		n *= 10
	case length > 3:
		length = min(length, maxSignificant)
		factor := 1
		for ; length > 3; length-- {
			factor *= 10
		}
		n /= factor
	}
	return n
}

func parseES5(s *dateScanner, day *dayComposer, tm *timeComposer, tz *zoneComposer) dateToken {
	invalid := dateToken{tag: tagInvalid}
	switch {
	case s.peek().isSign():
		signToken := s.take()
		if !s.peek().isFixed(6) {
			return signToken
		}
		year := s.take().value
		if signToken.sign() < 0 && year == 0 {
			return signToken
		}
		day.add(signToken.sign() * year)
	case s.peek().isFixed(4):
		day.add(s.take().value)
	default:
		return s.take()
	}
	if s.skipSymbol('-') {
		if !s.peek().isFixed(2) || !isMonth(s.peek().value) {
			return s.take()
		}
		day.add(s.take().value)
		if s.skipSymbol('-') {
			if !s.peek().isFixed(2) || !isDay(s.peek().value) {
				return s.take()
			}
			day.add(s.take().value)
		}
	}
	if !s.peek().isKeywordKind(kwTimeSeparator) {
		if s.peek().tag != tagEnd {
			return s.take()
		}
	} else {
		s.take()
		if !s.peek().isFixed(2) || !between(s.peek().value, 0, 24) {
			return invalid
		}
		hour24 := s.peek().value == 24
		tm.add(s.take().value)
		if !s.skipSymbol(':') {
			return invalid
		}
		if !s.peek().isFixed(2) || !isMinute(s.peek().value) || (hour24 && s.peek().value > 0) {
			return invalid
		}
		tm.add(s.take().value)
		if s.skipSymbol(':') {
			if !s.peek().isFixed(2) || !isMinute(s.peek().value) || (hour24 && s.peek().value > 0) {
				return invalid
			}
			tm.add(s.take().value)
			if s.skipSymbol('.') {
				if !s.peek().isNumber() || (hour24 && s.peek().value > 0) {
					return invalid
				}
				tm.add(readMillis(s.take()))
			}
		}
		if s.peek().isZ() {
			s.take()
			tz.set(0)
		} else if s.peek().isSign() {
			tz.setSign(s.take().sign())
			if s.peek().isFixed(4) {
				hourMin := s.take().value
				if !isHour(hourMin/100) || !isMinute(hourMin%100) {
					return invalid
				}
				tz.hour, tz.minute = hourMin/100, hourMin%100
			} else {
				if !s.peek().isFixed(2) || !isHour(s.peek().value) {
					return invalid
				}
				tz.hour = s.take().value
				if !s.skipSymbol(':') {
					return invalid
				}
				if !s.peek().isFixed(2) || !isMinute(s.peek().value) {
					return invalid
				}
				tz.minute = s.take().value
			}
		}
		if s.peek().tag != tagEnd {
			return invalid
		}
	}
	if tz.empty() && tm.empty() {
		tz.set(0)
	}
	day.iso = true
	return dateToken{tag: tagEnd}
}

func parseFields(text string) (fields [7]int, offset float64, ok bool) {
	s := newDateScanner(text)
	day := dayComposer{named: dNone}
	tm := timeComposer{hourOffset: dNone}
	tz := zoneComposer{sign: dNone, hour: dNone, minute: dNone}
	token := parseES5(s, &day, &tm, &tz)
	if token.tag == tagInvalid {
		return fields, 0, false
	}
	hasNumber := day.index > 0
	for ; token.tag != tagEnd; token = s.take() {
		switch {
		case token.isNumber():
			hasNumber = true
			n := token.value
			if s.skipSymbol(':') {
				if s.skipSymbol(':') {
					if !tm.empty() {
						return fields, 0, false
					}
					tm.add(n)
					tm.add(0)
				} else {
					if !tm.add(n) {
						return fields, 0, false
					}
					if s.peek().isSymbol('.') {
						s.take()
					}
				}
			} else if s.skipSymbol('.') && tm.expecting(n) {
				tm.add(n)
				if !s.peek().isNumber() {
					return fields, 0, false
				}
				ms := readMillis(s.take())
				if ms < 0 {
					return fields, 0, false
				}
				tm.addFinal(ms)
			} else if tz.expecting(n) {
				tz.minute = n
			} else if tm.expecting(n) {
				tm.addFinal(n)
				p := s.peek()
				if p.tag != tagEnd && p.tag != tagSpace && !p.isZ() && !p.isSign() {
					return fields, 0, false
				}
			} else {
				if !day.add(n) {
					return fields, 0, false
				}
				s.skipSymbol('-')
			}
		case token.tag == tagKeyword:
			switch {
			case token.kind == kwAmPm && !tm.empty():
				tm.hourOffset = token.value
			case token.kind == kwMonth:
				day.named = token.value
				s.skipSymbol('-')
			case token.kind == kwZone && hasNumber:
				tz.set(token.value)
			default:
				if hasNumber || s.peek().isNumber() {
					return fields, 0, false
				}
			}
		case token.isSign() && (tz.isUTC() || !tm.empty()):
			tz.setSign(token.sign())
			n, length := 0, 0
			if s.peek().isNumber() {
				next := s.take()
				n, length = next.value, next.length
			}
			hasNumber = true
			switch {
			case s.peek().isSymbol(':'):
				tz.hour, tz.minute = n, dNone
			case length == 1 || length == 2:
				tz.hour, tz.minute = n, 0
			case length == 3 || length == 4:
				tz.hour, tz.minute = n/100, n%100
			default:
				return fields, 0, false
			}
		case (token.isSign() || token.isSymbol(')')) && hasNumber:
			return fields, 0, false
		}
	}
	year, month, dayN, dayOK := day.write()
	if !dayOK {
		return fields, 0, false
	}
	clock, timeOK := tm.write()
	if !timeOK {
		return fields, 0, false
	}
	offset, zoneOK := tz.write()
	if !zoneOK {
		return fields, 0, false
	}
	return [7]int{year, month, dayN, clock[0], clock[1], clock[2], clock[3]}, offset, true
}

var monthDays = [2][12]int{
	{0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334},
	{0, 31, 60, 91, 121, 152, 182, 213, 244, 274, 305, 335},
}

func makeDay(year, month, date int) float64 {
	if year < -1000000 || year > 1000000 || month < -10000000 || month > 10000000 {
		return math.NaN()
	}
	y, m := year+month/12, month%12
	if m < 0 {
		m += 12
		y--
	}
	const delta = 399999
	base := 365*(1970+delta) + (1970+delta)/4 - (1970+delta)/100 + (1970+delta)/400
	days := 365*(y+delta) + (y+delta)/4 - (y+delta)/100 + (y+delta)/400 - base
	leap := 0
	if y%4 == 0 && (y%100 != 0 || y%400 == 0) {
		leap = 1
	}
	return float64(days+monthDays[leap][m]-1) + float64(date)
}

func zoneOffsetMs(utcMs float64) float64 {
	_, offset := time.UnixMilli(int64(utcMs)).In(time.Local).Zone()
	return float64(offset) * 1000
}

// localOffsetMs follows ICU's getOffsetFromLocal(FORMER, FORMER), which V8 uses: a repeated wall time takes the
// earlier instant, and a skipped one the offset in force before the change.
func localOffsetMs(localMs float64) float64 {
	before, after := zoneOffsetMs(localMs-864e5), zoneOffsetMs(localMs+864e5)
	if before == after {
		return before
	}
	best, found := math.Inf(-1), false
	for _, candidate := range [2]float64{before, after} {
		if zoneOffsetMs(localMs-candidate) == candidate {
			best, found = math.Max(best, candidate), true
		}
	}
	if found {
		return best
	}
	return zoneOffsetMs(localMs - math.Max(before, after))
}

// DateParse is JavaScript's Date.parse(String(value)) as V8 computes it; ok is false when the result is NaN or the
// conversion throws.
func DateParse(value any) (ms float64, ok bool, err error) {
	text, err := ToString(value)
	if err != nil {
		return 0, false, err
	}
	f, offset, valid := parseFields(text)
	if !valid {
		return 0, false, nil
	}
	day := makeDay(f[0], f[1], f[2])
	if day != day {
		return 0, false, nil
	}
	date := float64(day*864e5) + float64(f[3]*3600000+f[4]*60000+f[5]*1000+f[6])
	if offset != offset {
		if date < -maxTimeBeforeMs || date > maxTimeBeforeMs {
			return 0, false, nil
		}
		date -= localOffsetMs(date)
	} else {
		date -= offset * 1000
		if date < -maxTimeMs || date > maxTimeMs {
			return 0, false, nil
		}
	}
	if date < -maxTimeMs || date > maxTimeMs {
		return 0, false, nil
	}
	return math.Trunc(date) + 0, true, nil
}
