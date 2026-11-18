package server

import (
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestEvictionTakesFinishedThenQuietNeverBusy(t *testing.T) {
	l := CapListener(nil, 8)
	add := func(seq uint64, state int) *capConn {
		c := &capConn{cap: l, seq: seq, state: state}
		l.conns[c] = struct{}{}
		return c
	}
	add(1, stateBusy)
	add(2, stateUntracked)
	if v := l.evictable(); v != nil {
		t.Fatalf("a connection in state %d was picked; busy and untracked ones are never evicted", v.state)
	}
	idle, fresh := add(4, stateIdle), add(3, stateNew)
	if v := l.evictable(); v != fresh {
		t.Errorf("picked seq %d, want the older of the silent and the idle connection", v.seq)
	}
	delete(l.conns, fresh)
	if v := l.evictable(); v != idle {
		t.Errorf("picked %v, want the idle connection", v)
	}
	done := add(9, stateDone)
	if v := l.evictable(); v != done {
		t.Errorf("picked seq %d, want the half-closed connection before any idle one", v.seq)
	}
}

func TestHalfClosedConnectionIsFinishedAndTellsTheClient(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	l := CapListener(ln, 2)
	accept := func() (net.Conn, *capConn) {
		t.Helper()
		peer, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = peer.Close() })
		c, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return peer, c.(*capConn)
	}
	peer, c := accept()
	l.Track(c, http.StateActive)
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("client of a half-closed connection: %v, want EOF", err)
	}
	if v := l.evictable(); v != c {
		t.Errorf("picked %v, want the half-closed connection", v)
	}
	delete(l.conns, c)
	_, untracked := accept()
	_ = untracked.CloseWrite()
	if v := l.evictable(); v != nil {
		t.Error("a connection the ConnState hook never reported was picked")
	}
}

func TestAcceptWaitsWhileTheCloseWaitIsFull(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	l := CapListener(ln, 1)
	var parked []net.Conn
	for range l.parkedMax {
		peer, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer peer.Close()
		nc, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		c := nc.(*capConn)
		defer c.Close()
		l.Track(c, http.StateActive)
		_ = c.CloseWrite()
		_ = c.CloseWrite()
		parked = append(parked, c)
	}
	if l.parked != l.parkedMax || len(l.conns) != 1 {
		t.Fatalf("%d counted in the close wait and %d in slots, want %d and 1", l.parked, len(l.conns), l.parkedMax)
	}
	peer, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := l.Accept()
		accepted <- c
	}()
	select {
	case <-accepted:
		t.Fatal("Accept took a connection with the close wait full")
	case <-time.After(4 * closeWait / time.Duration(parkedPerConn)):
	}
	_ = parked[0].Close()
	_ = parked[0].Close()
	select {
	case c := <-accepted:
		defer c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Accept still waits after a goroutine left the close wait")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.parked != l.parkedMax-1 {
		t.Errorf("%d counted in the close wait after one of %d ended twice, want one less", l.parked, l.parkedMax)
	}
}
