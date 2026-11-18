package server

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

// Fake time stands still during synctest.Wait, so a frame seen there was sent by the push itself, not by a timer.
func TestPublishSendsASampleWhenItIsPushedWithoutATimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		r := memoryRunner(now, 1, time.Second)
		s, _ := newServer(t, nil, r)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.Publish(ctx, time.Hour)
		}()
		synctest.Wait()
		c := s.Hub.Open(ctx)
		defer c.Close()
		if len(c.frames) != 0 || len(c.pending) == 0 {
			t.Fatalf("before a push: %d queued frames, %d bytes of first frames", len(c.frames), len(c.pending))
		}
		at := now.Add(time.Second)
		r.MachineLive.Push(at, machineSample(at))
		synctest.Wait()
		if len(c.frames) != 1 {
			t.Fatalf("after a machine sample: %d frames, want 1 with no time passed", len(c.frames))
		}
		r.DockerLive.Push(at, dockerSample(at))
		r.DockerLive.Push(at.Add(time.Second), dockerSample(at.Add(time.Second)))
		synctest.Wait()
		if n := len(c.frames); n < 2 || n > 3 {
			t.Fatalf("after two docker samples: %d frames, want 2 or 3", n)
		}
		before := len(c.frames)
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if len(c.frames) != before {
			t.Fatalf("with no new sample: %d frames, want %d", len(c.frames), before)
		}
		cancel()
		<-done
	})
}
