package collect

import "time"

// Clock gives wall time for stored timestamps and a monotonic Elapsed for pacing; on macOS Elapsed
// pauses while the Mac sleeps and wall time does not, which is how a sleep is detected.
type Clock interface {
	Now() time.Time
	Elapsed() time.Duration
	NewTicker(d time.Duration) Ticker
	After(d time.Duration) <-chan time.Time
}

type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realClock struct{ start time.Time }

// RealClock returns UTC wall times with the monotonic reading stripped, so comparisons use wall time.
func RealClock() Clock { return realClock{start: time.Now()} }

func (c realClock) Now() time.Time                         { return time.Now().Round(0).UTC() }
func (c realClock) Elapsed() time.Duration                 { return time.Since(c.start) }
func (c realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (c realClock) NewTicker(d time.Duration) Ticker       { return realTicker{time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (t realTicker) C() <-chan time.Time { return t.t.C }
func (t realTicker) Stop()               { t.t.Stop() }
