package api

import (
	"errors"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

func d(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func dp(s string) *openapi_types.Date { v := openapi_types.Date{Time: d(s)}; return &v }

func rp(r TranscriptRange) *TranscriptRange { return &r }

func TestResolvePeriodRules(t *testing.T) {
	today := d("2026-11-08")
	first := d("2026-10-01")
	cases := []struct {
		name              string
		rng               *TranscriptRange
		from, to          *openapi_types.Date
		code              ErrorResponseCode
		wantFrom, wantTo  string
		prevFrom, prevTo  string
		partial, prevPart bool
	}{
		{name: "default is 30d", wantFrom: "2026-10-10", wantTo: "2026-11-08", prevFrom: "2026-09-10", prevTo: "2026-10-09",
			prevPart: true},
		{name: "today", rng: rp(TranscriptRangeToday), wantFrom: "2026-11-08", wantTo: "2026-11-08",
			prevFrom: "2026-11-07", prevTo: "2026-11-07"},
		{name: "90d reaches before history", rng: rp(TranscriptRangeN90d), wantFrom: "2026-08-11", wantTo: "2026-11-08",
			prevFrom: "2026-05-13", prevTo: "2026-08-10", partial: true, prevPart: true},
		{name: "all", rng: rp(TranscriptRangeAll), wantFrom: "2026-10-01", wantTo: "2026-11-08"},
		{name: "custom", from: dp("2026-11-01"), to: dp("2026-11-03"), wantFrom: "2026-11-01", wantTo: "2026-11-03",
			prevFrom: "2026-10-29", prevTo: "2026-10-31"},
		{name: "custom of 366 days", from: dp("2025-11-08"), to: dp("2026-11-08"), wantFrom: "2025-11-08",
			wantTo: "2026-11-08", prevFrom: "2024-11-07", prevTo: "2025-11-07", partial: true, prevPart: true},
		{name: "range with from", rng: rp(TranscriptRangeN7d), from: dp("2026-11-01"), to: dp("2026-11-02"),
			code: ErrorResponseCodeBadRange},
		{name: "unknown preset", rng: rp("3d"), code: ErrorResponseCodeBadRange},
		{name: "from alone", from: dp("2026-11-01"), code: ErrorResponseCodeBadDate},
		{name: "to alone", to: dp("2026-11-01"), code: ErrorResponseCodeBadDate},
		{name: "inverted", from: dp("2026-11-03"), to: dp("2026-11-01"), code: ErrorResponseCodeBadDate},
		{name: "future", from: dp("2026-11-08"), to: dp("2026-11-09"), code: ErrorResponseCodeBadDate},
		{name: "too early", from: dp("1999-12-31"), to: dp("2000-01-02"), code: ErrorResponseCodeBadDate},
		{name: "367 days", from: dp("2025-11-07"), to: dp("2026-11-08"), code: ErrorResponseCodeBadDate},
	}
	for _, c := range cases {
		p, err := ResolvePeriod(c.rng, c.from, c.to, today, &first)
		if c.code != "" {
			var pe *PeriodError
			if !errors.As(err, &pe) || pe.Code != c.code {
				t.Errorf("%s: err %v, want %s", c.name, err, c.code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		got := func(x *openapi_types.Date) string {
			if x == nil {
				return ""
			}
			return x.Format("2006-01-02")
		}
		if p.From.Format("2006-01-02") != c.wantFrom || p.To.Format("2006-01-02") != c.wantTo ||
			got(p.PreviousFrom) != c.prevFrom || got(p.PreviousTo) != c.prevTo ||
			p.Partial != c.partial || p.PreviousPartial != c.prevPart {
			t.Errorf("%s: got %s..%s prev %s..%s partial %v/%v", c.name, p.From.Format("2006-01-02"),
				p.To.Format("2006-01-02"), got(p.PreviousFrom), got(p.PreviousTo), p.Partial, p.PreviousPartial)
		}
		if int(p.Days) != int(d(c.wantTo).Sub(d(c.wantFrom)).Hours()/24)+1 {
			t.Errorf("%s: days %d", c.name, p.Days)
		}
	}
}

func TestMetricsWindowNeverAssumesAnInterval(t *testing.T) {
	for r, want := range map[MetricsRange]Resolution{
		MetricsRangeLive: ResolutionRaw, MetricsRangeN5m: ResolutionRaw, MetricsRangeN15m: ResolutionRaw,
		MetricsRangeN1h: ResolutionRaw, MetricsRangeN24h: ResolutionN1m, MetricsRangeN7d: ResolutionN1h,
	} {
		_, res, ok := MetricsWindow(r, false)
		if !ok || res != want {
			t.Errorf("%s: %s, want %s", r, res, want)
		}
		span, res, _ := MetricsWindow(r, true)
		if res != ResolutionRaw || span > time.Hour {
			t.Errorf("%s paused: %s over %s", r, res, span)
		}
	}
	if _, _, ok := MetricsWindow("10s", false); ok {
		t.Error("unknown range accepted")
	}
}
