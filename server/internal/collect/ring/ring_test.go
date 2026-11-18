package ring

import (
	"sync"
	"testing"
	"time"
)

func TestEvictsAfterOneHour(t *testing.T) {
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	r := New[int](1000, time.Hour)
	for i := 0; i <= 360; i++ {
		r.Push(t0.Add(time.Duration(i)*10*time.Second), i)
	}
	now := t0.Add(time.Hour)
	w := r.Window(now)
	if len(w) != 360 || r.Len() != 360 {
		t.Fatalf("window %d len %d, want 360", len(w), r.Len())
	}
	if w[0].V != 1 || w[len(w)-1].V != 360 {
		t.Fatalf("first %d last %d, want 1 and 360", w[0].V, w[len(w)-1].V)
	}
	if got := r.Window(now.Add(30 * time.Minute)); len(got) != 180 {
		t.Fatalf("stalled source after 30 min: %d entries, want 180", len(got))
	}
	if got := r.Window(now.Add(2 * time.Hour)); len(got) != 0 {
		t.Fatalf("stalled source after 2 h: %d entries, want 0", len(got))
	}
}

func TestCapacityWraps(t *testing.T) {
	t0 := time.Unix(0, 0)
	r := New[int](3, 0)
	for i := 0; i < 10; i++ {
		r.Push(t0.Add(time.Duration(i)*time.Second), i)
	}
	w := r.Window(t0)
	if len(w) != 3 || w[0].V != 7 || w[2].V != 9 {
		t.Fatalf("got %+v", w)
	}
	if e, ok := r.Latest(); !ok || e.V != 9 {
		t.Fatalf("latest %+v %v", e, ok)
	}
}

func TestConcurrentReaders(t *testing.T) {
	r := New[[]int](360, time.Hour)
	t0 := time.Now()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				for _, e := range r.Window(t0.Add(time.Hour)) {
					_ = len(e.V)
				}
				r.Latest()
			}
		}()
	}
	for i := 0; i < 2000; i++ {
		r.Push(t0.Add(time.Duration(i)*time.Second), []int{i})
	}
	wg.Wait()
}

func TestPushedHoldsOneTokenForAnyNumberOfPushes(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	r := New[int](8, time.Hour)
	select {
	case <-r.Pushed():
		t.Fatal("a token before the first push")
	default:
	}
	for i := 0; i < 5; i++ {
		r.Push(t0.Add(time.Duration(i)*time.Second), i)
	}
	select {
	case <-r.Pushed():
	default:
		t.Fatal("no token after pushes")
	}
	if e, ok := r.Latest(); !ok || e.V != 4 {
		t.Fatalf("latest after the token: %v %v, want 4", e.V, ok)
	}
	select {
	case <-r.Pushed():
		t.Fatal("a second token for the same pushes")
	default:
	}
}
