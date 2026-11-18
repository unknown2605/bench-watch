package aggregate

import (
	"math/rand/v2"
	"regexp"
	"testing"
	"time"

	"benchwatch/server/internal/extract"
)

func TestIsHash16IsTheRegexp(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{16}$`)
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := "0123456789abcdefABCDEFgz\n -"
	cases := []string{"", "0123456789abcdef", "0123456789abcdef\n", "0123456789ABCDEF", "0123456789abcde"}
	for range 20000 {
		b := make([]byte, 14+rng.IntN(4))
		for i := range b {
			b[i] = alphabet[rng.IntN(len(alphabet))]
			if rng.IntN(3) > 0 {
				b[i] = alphabet[rng.IntN(16)]
			}
		}
		cases = append(cases, string(b))
	}
	for _, c := range cases {
		if isHash16(c) != re.MatchString(c) {
			t.Fatalf("isHash16(%q) = %v", c, isHash16(c))
		}
	}
}

func TestDayOfIsTheFormattedLocalDay(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	slow := func(sec int64) time.Time {
		d, err := time.ParseInLocation("2006-01-02", LocalDay(sec), time.UTC)
		if err != nil {
			return time.Unix(sec, 0).UTC().Truncate(24 * time.Hour)
		}
		return d
	}
	rng := rand.New(rand.NewPCG(3, 4))
	for _, name := range []string{"UTC", "America/New_York", "Asia/Kathmandu", "Australia/Lord_Howe", "Pacific/Chatham",
		"America/Santiago", "Asia/Ho_Chi_Minh"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		time.Local = loc
		for i := range 50000 {
			sec := extract.MinTs + rng.Int64N(extract.MaxTs-extract.MinTs+1)
			if i%2 == 0 {
				sec = time.Date(2026, time.Month(1+rng.IntN(12)), 1+rng.IntN(28), 0, 0, 0, 0, loc).Unix() +
					rng.Int64N(7200) - 3600
			}
			if got, want := dayOf(sec), slow(sec); !got.Equal(want) || got.Location() != want.Location() {
				t.Fatalf("%s: dayOf(%d) = %v, want %v", name, sec, got, want)
			}
		}
	}
}
