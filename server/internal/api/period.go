package api

import (
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// Limits of a custom transcript period (api/openapi.yaml, Transcript periods).
const (
	MaxPeriodDays = 366
	DefaultRange  = TranscriptRangeN30d
)

var earliestDay = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

var presetDays = map[TranscriptRange]int{
	TranscriptRangeToday: 1,
	TranscriptRangeN7d:   7,
	TranscriptRangeN14d:  14,
	TranscriptRangeN30d:  30,
	TranscriptRangeN90d:  90,
}

type PeriodError struct{ Code ErrorResponseCode }

func (e *PeriodError) Error() string { return string(e.Code) }

func day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

func date(t time.Time) openapi_types.Date { return openapi_types.Date{Time: t} }

// ResolvePeriod applies the spec's period rules. today is the server's local calendar day and firstDay the first
// day with history (nil when there is none); both are read as calendar days, whatever their location.
func ResolvePeriod(rng *TranscriptRange, from, to *openapi_types.Date, today time.Time, firstDay *time.Time) (Period, error) {
	today = day(today)
	var lo, hi time.Time
	all := false
	switch {
	case (from != nil || to != nil) && rng != nil:
		return Period{}, &PeriodError{ErrorResponseCodeBadRange}
	case from != nil && to != nil:
		lo, hi = day(from.Time), day(to.Time)
		if lo.Before(earliestDay) || hi.Before(lo) || hi.After(today) || int(hi.Sub(lo).Hours()/24)+1 > MaxPeriodDays {
			return Period{}, &PeriodError{ErrorResponseCodeBadDate}
		}
	case from != nil || to != nil:
		return Period{}, &PeriodError{ErrorResponseCodeBadDate}
	default:
		r := DefaultRange
		if rng != nil {
			r = *rng
		}
		hi = today
		if r == TranscriptRangeAll {
			all = true
			lo = today
			if firstDay != nil && day(*firstDay).Before(today) {
				lo = day(*firstDay)
			}
		} else if n, ok := presetDays[r]; ok {
			lo = today.AddDate(0, 0, 1-n)
		} else {
			return Period{}, &PeriodError{ErrorResponseCodeBadRange}
		}
	}
	days := int(hi.Sub(lo).Hours()/24) + 1
	before := func(t time.Time) bool { return firstDay == nil || t.Before(day(*firstDay)) }
	p := Period{From: date(lo), To: date(hi), Days: int32(days), Partial: before(lo)}
	if !all {
		prevTo := lo.AddDate(0, 0, -1)
		prevFrom := prevTo.AddDate(0, 0, 1-days)
		pf, pt := date(prevFrom), date(prevTo)
		p.PreviousFrom, p.PreviousTo = &pf, &pt
		p.PreviousPartial = before(prevFrom)
	} else {
		p.Partial = firstDay == nil
	}
	return p, nil
}

// Spans and resolutions follow api/openapi.yaml, Metric ranges; while history is paused, every range is served
// from the in-memory last hour at raw resolution.
func MetricsWindow(r MetricsRange, historyPaused bool) (time.Duration, Resolution, bool) {
	var span time.Duration
	res := ResolutionRaw
	switch r {
	case MetricsRangeLive, MetricsRangeN5m:
		span = 5 * time.Minute
	case MetricsRangeN15m:
		span = 15 * time.Minute
	case MetricsRangeN1h:
		span = time.Hour
	case MetricsRangeN24h:
		span, res = 24*time.Hour, ResolutionN1m
	case MetricsRangeN7d:
		span, res = 7*24*time.Hour, ResolutionN1h
	default:
		return 0, "", false
	}
	if historyPaused {
		return min(span, time.Hour), ResolutionRaw, true
	}
	return span, res, true
}
