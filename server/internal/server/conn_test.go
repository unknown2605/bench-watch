package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// liveServer serves h the way cmd/benchwatch does: same timeouts, connection cap and ConnState hook.
func liveServer(t *testing.T, h http.Handler, conns int) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	capped := CapListener(ln, conns)
	hs := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: ErrorLog, ConnState: capped.Track}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hs.Serve(capped)
	}()
	t.Cleanup(func() {
		_ = hs.Close()
		<-done
	})
	return ln.Addr().String()
}

// firstLine sends raw and returns the status line, or the read error as text.
func firstLine(t *testing.T, addr, raw string) string {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return "dial: " + err.Error()
	}
	defer c.Close()
	_, _ = io.WriteString(c, raw)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	l, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "read: " + err.Error()
	}
	return strings.TrimSpace(l)
}

func plainGet(target string) string {
	return fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, AllowedHosts[0])
}

// The answer is above net/http's 2 KiB buffer, so on writing it the server would drain the announced body first.
func TestDripFedBodiesHoldNoSlot(t *testing.T) {
	now := time.Now()
	s, h := newServer(t, nil, memoryRunner(now, 360, 10*time.Second))
	s.Now = func() time.Time { return now }
	addr := liveServer(t, h, MaxConns)
	const target = "/api/machine?range=24h"
	stop := make(chan struct{})
	var wg sync.WaitGroup
	defer func() {
		close(stop)
		wg.Wait()
	}()
	answers := make(chan string, 8)
	for range 8 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 200000\r\n\r\n", target, AllowedHosts[0])
		wg.Add(2)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(100 * time.Millisecond):
					if _, err := c.Write([]byte("x")); err != nil {
						return
					}
				}
			}
		}()
		go func() {
			defer wg.Done()
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			br := bufio.NewReader(c)
			l, err := br.ReadString('\n')
			if err != nil {
				answers <- "read: " + err.Error()
				return
			}
			// A reset counts as closed too: the client is still sending the body it announced.
			_, err = io.Copy(io.Discard, br)
			var ne net.Error
			answers <- strings.TrimSpace(l) + fmt.Sprintf(" closed=%v", !errors.As(err, &ne) || !ne.Timeout())
		}()
	}
	for range 8 {
		if a := <-answers; a != "HTTP/1.1 400 Bad Request closed=true" {
			t.Errorf("drip-fed GET: %s, want a 400 and the connection closed", a)
		}
	}
	if l := firstLine(t, addr, plainGet(target)); l != "HTTP/1.1 200 OK" {
		t.Errorf("ninth, normal GET: %s", l)
	}
	for name, raw := range map[string]string{
		"HEAD with a body": fmt.Sprintf("HEAD /api/status HTTP/1.1\r\nHost: %s\r\nContent-Length: 1\r\n\r\nx",
			AllowedHosts[0]),
		"chunked GET": fmt.Sprintf("GET /api/status HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n",
			AllowedHosts[0]),
		"other host": "GET /api/status HTTP/1.1\r\nHost: nope.example\r\nContent-Length: 9\r\n\r\n",
	} {
		if l := firstLine(t, addr, raw); l != "HTTP/1.1 400 Bad Request" {
			t.Errorf("%s: %s, want 400", name, l)
		}
	}
}

// stallDB answers the schema check, then holds every other query until its context ends.
type stallDB struct{ fakeDB }

type stallRow struct{ ctx context.Context }

func (r stallRow) Scan(...any) error {
	<-r.ctx.Done()
	return r.ctx.Err()
}

func (d *stallDB) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	if strings.Contains(q, "goose_db_version") {
		return d.fakeDB.QueryRow(ctx, q, args...)
	}
	return stallRow{ctx}
}

func (d *stallDB) Query(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRequestReadDeadlineEndsAStalledRequest(t *testing.T) {
	s, _ := newServer(t, &stallDB{fakeDB{versions: Migrations}}, nil)
	s.QueryTimeout = 20 * time.Second
	s.ReadTimeout = 300 * time.Millisecond
	s.Hub.Ping = 50 * time.Millisecond
	h, err := Handler(s)
	if err != nil {
		t.Fatal(err)
	}
	addr := liveServer(t, h, MaxConns)
	stream := rawStream(t, addr)
	defer stream.Close()
	start := time.Now()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "GET /api/meta HTTP/1.1\r\nHost: %s\r\n\r\n", AllowedHosts[0])
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("stalled request: %v after %v", err, time.Since(start))
	}
	resp.Body.Close()
	if d := time.Since(start); d > 3*time.Second || !resp.Close {
		t.Errorf("stalled request ended after %v with close=%v, want within 3 s and the connection closed", d,
			resp.Close)
	}
	// The event stream has no read deadline: it still delivers heartbeats well past ReadTimeout.
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(stream)
	pings := 0
	for pings < 3 {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended after %v: %v", time.Since(start), err)
		}
		if strings.HasPrefix(l, ": ping") && time.Since(start) > s.ReadTimeout {
			pings++
		}
	}
}

func TestConcurrentOpensNeverPassTheHubCap(t *testing.T) {
	hub, err := NewHub()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range maxClients {
		hub.Open(ctx)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	most := 0
	start := make(chan struct{})
	for range 48 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			hub.Open(ctx)
			n := hub.Clients()
			mu.Lock()
			most = max(most, n)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	if most > maxClients || hub.Clients() != maxClients {
		t.Errorf("up to %d streams open, %d at the end; the cap is %d", most, hub.Clients(), maxClients)
	}
}

func TestSilentConnectionsCannotLockClientsOut(t *testing.T) {
	s, h := newServer(t, nil, nil)
	s.Hub.Ping = 20 * time.Millisecond
	addr := liveServer(t, h, 4)
	var silent []net.Conn
	for range 4 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		silent = append(silent, c)
	}
	time.Sleep(100 * time.Millisecond)
	if l := firstLine(t, addr, plainGet("/api/status")); l != "HTTP/1.1 200 OK" {
		t.Fatalf("GET with every slot held by silent connections: %s", l)
	}
	_ = silent[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := silent[0].Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("oldest silent connection: %v, want it closed", err)
	}

	for _, c := range silent {
		_ = c.Close()
	}
	var streams []net.Conn
	for range 4 {
		c := rawStream(t, addr)
		defer c.Close()
		if l, err := bufio.NewReader(c).ReadString('\n'); err != nil || !strings.Contains(l, "200") {
			t.Fatalf("stream: %q %v", l, err)
		}
		streams = append(streams, c)
	}
	if l := firstLine(t, addr, plainGet("/api/status")); strings.HasPrefix(l, "HTTP/1.1 200") {
		t.Errorf("a full listener of streams took one more connection: %s", l)
	}
	for i, c := range streams {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err != nil {
			t.Errorf("stream %d was closed for a new connection: %v", i, err)
		}
	}
}

// CM-00010: net/http keeps a refused connection 500 ms; with every slot in that wait, clients were locked out.
// Workers leave after the status line, as a flood does: waiting for the end would pace them to 2 a second each.
func TestRefusedRequestsCannotLockClientsOut(t *testing.T) {
	_, h := newServer(t, nil, nil)
	host := AllowedHosts[0]
	for name, raw := range map[string]string{
		"GET with a body": fmt.Sprintf("GET /api/status HTTP/1.1\r\nHost: %s\r\nContent-Length: 300000\r\n\r\n", host),
		"POST with a body": fmt.Sprintf("POST /api/status HTTP/1.1\r\nHost: %s\r\nContent-Length: 300000\r\n\r\n",
			host),
		"chunked GET": fmt.Sprintf("GET /api/status HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\n\r\n", host),
		"header past the limit": fmt.Sprintf("GET /api/status HTTP/1.1\r\nHost: %s\r\nX-Pad: %s\r\n\r\n", host,
			strings.Repeat("a", 32<<10)),
	} {
		t.Run(name, func(t *testing.T) {
			addr := liveServer(t, h, MaxConns)
			stop := make(chan struct{})
			var wg sync.WaitGroup
			defer func() {
				close(stop)
				wg.Wait()
			}()
			for range refusedWorkers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						case <-time.After(time.Millisecond):
						}
						c, err := net.Dial("tcp", addr)
						if err != nil {
							continue
						}
						_, _ = io.WriteString(c, raw)
						_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
						_, _ = bufio.NewReader(c).ReadString('\n')
						// A reset leaves no TIME_WAIT entry, so the loop cannot run the machine out of ports.
						_ = c.(*net.TCPConn).SetLinger(0)
						_ = c.Close()
					}
				}()
			}
			time.Sleep(200 * time.Millisecond)
			for i := range 25 {
				if l := firstLine(t, addr, plainGet("/api/status")); l != "HTTP/1.1 200 OK" {
					t.Fatalf("GET %d beside %d looping refused-request workers: %s", i, refusedWorkers, l)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

// Far under MaxConns on purpose: workers equal to the cap can all be mid-request at one instant, which no eviction
// rule survives (a GET was then lost in about 1 of 190). 8 workers filled all 64 slots before the fix.
const refusedWorkers = 8

// parkedGoroutines counts connection goroutines inside net/http's 500 ms wait after a half-close.
func parkedGoroutines(buf []byte) int {
	return bytes.Count(buf[:runtime.Stack(buf, true)], []byte("net/http.(*conn).closeWriteAndWait("))
}

// CM-00010 Q7: an evicted refused connection freed its slot while net/http kept its goroutine for 500 ms, so a
// flood parked its rate x 0.5 s of them (10 thousand, 230 MB) beside a full set of slots.
func TestRefusedFloodParksFewGoroutines(t *testing.T) {
	const conns, workers, each = 8, 4, 40
	stacks := make([]byte, 4<<20)
	for end := time.Now().Add(5 * time.Second); parkedGoroutines(stacks) > 0; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("goroutines of an earlier test are still in the close wait")
		}
	}
	_, h := newServer(t, nil, nil)
	addr := liveServer(t, h, conns)
	raw := fmt.Sprintf("POST /api/status HTTP/1.1\r\nHost: %s\r\nContent-Length: 300000\r\n\r\n", AllowedHosts[0])
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				c, err := net.Dial("tcp", addr)
				if err != nil {
					continue
				}
				_, _ = io.WriteString(c, raw)
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, _ = bufio.NewReader(c).ReadString('\n')
				_ = c.(*net.TCPConn).SetLinger(0)
				_ = c.Close()
			}
		}()
	}
	flood := make(chan struct{})
	go func() {
		wg.Wait()
		close(flood)
	}()
	peak := 0
	for over := false; !over; {
		select {
		case <-flood:
			over = true
		case <-time.After(5 * time.Millisecond):
		}
		peak = max(peak, parkedGoroutines(stacks))
	}
	// Each connection in a slot can still half-close after Accept stopped taking new ones.
	if limit := conns*parkedPerConn + conns; peak > limit {
		t.Errorf("%d refused requests parked %d goroutines at once, want at most %d", workers*each, peak, limit)
	}
	if peak < conns {
		t.Errorf("only %d goroutines seen in the close wait: the flood or the count no longer works", peak)
	}
}

func TestIdleConnectionsGiveWayToNewOnes(t *testing.T) {
	_, h := newServer(t, nil, nil)
	addr := liveServer(t, h, 4)
	var idle []net.Conn
	for range 4 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "GET /api/status HTTP/1.1\r\nHost: %s\r\n\r\n", AllowedHosts[0])
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil || resp.StatusCode != http.StatusOK || resp.Close {
			t.Fatalf("keep-alive GET: %v %+v", err, resp)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		idle = append(idle, c)
	}
	// net/http reports a connection idle just after it flushed the answer, so the first try can come too early.
	waitFor(t, func() bool { return firstLine(t, addr, plainGet("/api/status")) == "HTTP/1.1 200 OK" })
	_ = idle[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := idle[0].Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("oldest idle connection: %v, want it closed for the new one", err)
	}
}

// A read deadline on the stream would cancel its request once it expired, so a frame published later never arrives.
func TestEventStreamOutlivesTheReadDeadline(t *testing.T) {
	s, _ := newServer(t, nil, nil)
	s.ReadTimeout = 100 * time.Millisecond
	h, err := Handler(s)
	if err != nil {
		t.Fatal(err)
	}
	stream := rawStream(t, liveServer(t, h, MaxConns))
	defer stream.Close()
	waitFor(t, func() bool { return s.Hub.Clients() == 1 })
	time.Sleep(4 * s.ReadTimeout)
	s.Hub.Status(s.StatusNow(context.Background()))
	_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
	br := bufio.NewReader(stream)
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the frame published %v after it opened: %v", 4*s.ReadTimeout, err)
		}
		if strings.HasPrefix(l, "event: status") {
			return
		}
	}
}

// CM-00010: a flood of opens used to push out the oldest stream, the page's own, within 16 opens.
func TestBurstOfOpensEvictsItselfNotAnOlderStream(t *testing.T) {
	hub, err := NewHub()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page := hub.Open(ctx)
	var last *client
	for range 20 * maxClients {
		last = hub.Open(ctx)
	}
	for name, c := range map[string]*client{"the stream open before the burst": page, "the newest stream": last} {
		select {
		case <-c.gone:
			t.Errorf("%s was closed", name)
		default:
		}
	}
	if hub.Clients() != maxClients {
		t.Errorf("%d streams open, want %d", hub.Clients(), maxClients)
	}
}
