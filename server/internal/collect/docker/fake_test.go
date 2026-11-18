package docker

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// fakeAPI is an in-memory daemon for tests under testing/synctest, where a real socket would keep fake time still.
type fakeAPI struct {
	mu       sync.Mutex
	pingErr  error
	listErr  error
	statsErr error
	items    []container.Summary
	opens    int
	peak     int
	writers  []*io.PipeWriter
	// onOpen runs in its own goroutine for each stats stream opened; n counts from 1.
	onOpen func(n int, w *io.PipeWriter)
}

var fakeEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fakeFrame is stats record k: one second apart, 10% of one CPU and 1000 bytes per second received.
func fakeFrame(k int) string {
	return fmt.Sprintf(`{"read":%q,"cpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":1},`+
		`"precpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d},`+
		`"memory_stats":{"usage":100,"limit":1000},"networks":{"eth0":{"rx_bytes":%d,"tx_bytes":0}}}`+"\n",
		fakeEpoch.Add(time.Duration(k)*time.Second).Format(time.RFC3339Nano),
		k*1e8, k*1e9, (k-1)*1e8, (k-1)*1e9, k*1000)
}

func fakeRunning(n int) []container.Summary {
	items := make([]container.Summary, n)
	for i := range items {
		items[i] = container.Summary{ID: fmt.Sprintf("c%d", i+1), Names: []string{fmt.Sprintf("/n%d", i+1)},
			State: container.StateRunning}
	}
	return items
}

// sendFrames writes records 1..n into w and keeps the stream open.
func sendFrames(n int) func(int, *io.PipeWriter) {
	return func(_ int, w *io.PipeWriter) {
		for k := 1; k <= n; k++ {
			if _, err := w.Write([]byte(fakeFrame(k))); err != nil {
				return
			}
		}
	}
}

func (f *fakeAPI) Ping(context.Context, client.PingOptions) (client.PingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.PingResult{}, f.pingErr
}

func (f *fakeAPI) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return client.ContainerListResult{}, f.listErr
	}
	return client.ContainerListResult{Items: append([]container.Summary(nil), f.items...)}, nil
}

func (f *fakeAPI) ContainerStats(ctx context.Context, _ string,
	_ client.ContainerStatsOptions) (client.ContainerStatsResult, error) {
	f.mu.Lock()
	f.peak = max(f.peak, runtime.NumGoroutine())
	if f.statsErr != nil {
		f.opens++
		defer f.mu.Unlock()
		return client.ContainerStatsResult{}, f.statsErr
	}
	f.opens++
	n := f.opens
	pr, pw := io.Pipe()
	f.writers = append(f.writers, pw)
	on := f.onOpen
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		pw.CloseWithError(ctx.Err())
	}()
	if on != nil {
		go on(n, pw)
	}
	return client.ContainerStatsResult{Body: pr}, nil
}

func (f *fakeAPI) Events(context.Context, client.EventsListOptions) client.EventsResult {
	return client.EventsResult{}
}

func (f *fakeAPI) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return client.SystemInfoResult{}, nil
}

func (f *fakeAPI) DiskUsage(context.Context, client.DiskUsageOptions) (client.DiskUsageResult, error) {
	return client.DiskUsageResult{}, nil
}

func (f *fakeAPI) Close() error { return nil }

func (f *fakeAPI) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *fakeAPI) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
}
