package docker

import (
	"context"
	"errors"
	"io"
	"runtime"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"benchwatch/server/internal/collect"
)

func syncSnap(t *testing.T, c *Collector) Snapshot {
	t.Helper()
	s, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A stream that goes quiet stops counting as current after StreamFresh, long before StreamIdle reopens it.
func TestAQuietStreamStopsCountingAsCurrentBeforeItReopens(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api := &fakeAPI{items: fakeRunning(1), onOpen: sendFrames(2)}
		c := newCollector(api, Options{})
		defer c.Close()
		if s := syncSnap(t, c); !s.containers[0].statsOK {
			t.Fatalf("fresh record: %v", s.Partial)
		}
		time.Sleep(2 * time.Second)
		if s := syncSnap(t, c); !s.containers[0].statsOK {
			t.Fatalf("a record 2 s old must still count: %v", s.Partial)
		}
		time.Sleep(2 * time.Second)
		s := syncSnap(t, c)
		if s.containers[0].statsOK || len(s.Partial) == 0 || s.Partial[0].Code != "ETIMEDOUT" {
			t.Fatalf("a record 4 s old must be missing with the idle code: %+v", s.Partial)
		}
		if n := api.openCount(); n != 1 {
			t.Fatalf("%d opens: the stream must keep running until its own idle limit", n)
		}
	})
}

// A stream that stops sending is reopened after StreamIdle, and its old record no longer counts.
func TestIdleStreamReopensAndGoesStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api := &fakeAPI{items: fakeRunning(1), onOpen: func(n int, w *io.PipeWriter) {
			if n == 1 {
				sendFrames(2)(n, w)
			}
		}}
		c := newCollector(api, Options{StreamIdle: 200 * time.Millisecond, StreamBackoff: 20 * time.Millisecond})
		defer c.Close()
		syncSnap(t, c)
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		if n := api.openCount(); n != 2 {
			t.Fatalf("%d opens after the idle limit, want 2", n)
		}
		s := syncSnap(t, c)
		if len(s.Partial) == 0 || s.Partial[0].Code != "ETIMEDOUT" {
			t.Fatalf("idle stream: %v", s.Partial)
		}
	})
}

// A stream the daemon drops reopens with a doubling wait; once records flow again the wait starts over.
func TestStreamReconnectsWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api := &fakeAPI{items: fakeRunning(1), statsErr: errors.New("boom")}
		c := newCollector(api, Options{StreamBackoff: 100 * time.Millisecond, MaxStreamBackoff: 800 * time.Millisecond})
		defer c.Close()
		syncSnap(t, c)
		time.Sleep(1600 * time.Millisecond)
		synctest.Wait()
		// Waits of 100, 200, 400 and 800 ms put reopens at 0, 0.1, 0.3, 0.7 and 1.5 s.
		if n := api.openCount(); n != 5 {
			t.Fatalf("%d opens in 1.6 s, want 5", n)
		}
		if s := syncSnap(t, c); len(s.Partial) == 0 || s.Partial[0].Code != "EOTHER" || s.containers[0].statsOK {
			t.Fatalf("a failing stream must show as missing stats: %v", s.Partial)
		}
		api.set(func() { api.statsErr, api.onOpen = nil, sendFrames(2) })
		time.Sleep(800 * time.Millisecond)
		synctest.Wait()
		if s := syncSnap(t, c); len(s.Partial) != 0 || !s.containers[0].statsOK {
			t.Fatalf("after recovery: %v", s.Partial)
		}
		opens := api.openCount()
		api.mu.Lock()
		w := api.writers[len(api.writers)-1]
		api.mu.Unlock()
		w.CloseWithError(io.EOF)
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()
		if n := api.openCount() - opens; n != 1 {
			t.Fatalf("%d reopens 150 ms after a cut: records flowed, so the wait must have started over", n)
		}
	})
}

// With 2000 running containers only MaxStreams streams open, and goroutines stay bounded by that cap.
func TestStatsStreamsAreCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api := &fakeAPI{items: fakeRunning(2000), onOpen: sendFrames(2)}
		c := newCollector(api, Options{MaxStreams: 4})
		defer c.Close()
		base := runtime.NumGoroutine()
		s, err := c.Snapshot(context.Background())
		synctest.Wait()
		if err != nil || s.Count() != 2000 || len(s.Partial) == 0 || s.Partial[0].Code != "ETOOLARGE" {
			t.Fatalf("2000 running containers past the stream cap: %v %v", err, s.Partial)
		}
		if open, _ := c.Streams(); open != 4 {
			t.Fatalf("%d streams open for a cap of 4", open)
		}
		if peak := max(api.peak, runtime.NumGoroutine()); peak-base > 4*6+8 {
			t.Fatalf("goroutines went from %d to %d for 4 streams", base, peak)
		}
	})
}

// A list that times out on a slow daemon leaves the streams to their own idle and backoff handling.
func TestSlowDaemonKeepsStreams(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api := &fakeAPI{items: fakeRunning(1), onOpen: sendFrames(2)}
		c := newCollector(api, Options{})
		defer c.Close()
		syncSnap(t, c)
		api.set(func() { api.listErr = context.DeadlineExceeded })
		_, err := c.Snapshot(context.Background())
		if collect.ErrorCode(err) != "ETIMEDOUT" {
			t.Fatalf("a timed-out list must fail the snapshot: %v", err)
		}
		api.set(func() { api.listErr = nil })
		syncSnap(t, c)
		if open, _ := c.Streams(); open != 1 || api.openCount() != 1 {
			t.Fatalf("%d streams open, %d opens in all: a timeout must not close and reopen", open, api.openCount())
		}
	})
}

// A daemon that refuses connections closes the streams, whether the ping or the list reports it.
func TestHardUnavailabilityClosesStreams(t *testing.T) {
	for name, fail := range map[string]func(*fakeAPI){
		"list": func(a *fakeAPI) { a.listErr = syscall.ECONNREFUSED },
		"ping": func(a *fakeAPI) { a.pingErr = syscall.ECONNREFUSED },
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				api := &fakeAPI{items: fakeRunning(1), onOpen: sendFrames(2)}
				c := newCollector(api, Options{})
				defer c.Close()
				syncSnap(t, c)
				api.set(func() { fail(api) })
				c.negotiated.Store(name == "list")
				if _, err := c.Snapshot(context.Background()); err == nil {
					t.Fatal("an unreachable daemon must fail the snapshot")
				}
				if open, _ := c.Streams(); open != 0 {
					t.Fatalf("%d streams left open", open)
				}
			})
		})
	}
}

// Snapshot after Close fails instead of opening streams nothing will wait for.
func TestSnapshotAfterCloseFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		api := &fakeAPI{items: fakeRunning(1), onOpen: sendFrames(2)}
		c := newCollector(api, Options{})
		c.Close()
		if _, err := c.Snapshot(context.Background()); err == nil {
			t.Fatal("Snapshot after Close must fail")
		}
		if n := api.openCount(); n != 0 {
			t.Fatalf("%d streams opened after Close", n)
		}
	})
}
