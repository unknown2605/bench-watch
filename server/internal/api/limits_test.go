package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type slowMeta struct {
	stub
	entered chan struct{}
	release chan struct{}
}

func (s slowMeta) GetMeta(ctx context.Context, r GetMetaRequestObject) (GetMetaResponseObject, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.stub.GetMeta(ctx, r)
}

func TestRequestsPastTheCapAnswerUnavailable(t *testing.T) {
	s := slowMeta{stub: stub{meta: validMeta()}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	h, err := NewHandlerWith(s, HandlerOptions{MaxRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
			codes <- rec.Code
		}()
	}
	<-s.entered
	<-s.entered
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	if rec.Code != http.StatusServiceUnavailable || codeOf(t, rec.Body.String()) != ErrorResponseCodeUnavailable {
		t.Errorf("third request: %d %s, want 503 unavailable", rec.Code, rec.Body.String())
	}
	close(s.release)
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK {
			t.Errorf("a request inside the cap got %d", c)
		}
	}
}

func codeOf(t *testing.T, body string) ErrorResponseCode {
	t.Helper()
	var e ErrorResponse
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("body %q", body)
	}
	return e.Code
}

// Every 500 reaches OnError with the route template and an error that has a code.
func TestHandlerFailuresReachOnError(t *testing.T) {
	type call struct {
		route string
		err   error
	}
	var got []call
	opt := HandlerOptions{OnError: func(route string, err error) { got = append(got, call{route, err}) }}
	broken := validMeta()
	broken.Projects = []ProjectLabel{"/Users/secret/project"}
	for _, tc := range []struct {
		s      StrictServerInterface
		target string
	}{{stub{}, "/api/status"}, {stub{meta: broken}, "/api/meta"}} {
		h, err := NewHandlerWith(tc.s, opt)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500", tc.target, rec.Code)
		}
	}
	if len(got) != 2 || got[0].route != "/api/status" || got[0].err == nil || got[1].route != "/api/meta" ||
		!errors.Is(got[1].err, ErrBadResponse) {
		t.Fatalf("OnError calls %+v", got)
	}
}

func TestLiveWriterAbortStopsWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	lw := &liveWriter{ResponseWriter: rec, rc: http.NewResponseController(rec), timeout: time.Second}
	if _, err := lw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	lw.abort()
	if _, err := lw.Write([]byte("y")); err == nil {
		t.Error("write after abort succeeded")
	}
	lw.finish()
	if rec.Body.String() != "x" {
		t.Errorf("body %q", rec.Body.String())
	}
}

// stalledWriter takes the status line, then blocks every body write until release closes.
type stalledWriter struct {
	*httptest.ResponseRecorder
	writing chan struct{}
	release chan struct{}
}

func (w stalledWriter) Write(b []byte) (int, error) {
	w.writing <- struct{}{}
	<-w.release
	return w.ResponseRecorder.Write(b)
}

func TestSlowReaderHoldsNoRequestSlot(t *testing.T) {
	h, err := NewHandlerWith(stub{meta: validMeta()}, HandlerOptions{MaxRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	slow := stalledWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(slow, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	}()
	<-slow.writing
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	close(slow.release)
	<-done
	if rec.Code != http.StatusOK {
		t.Errorf("request while another answer is being written: %d, want 200", rec.Code)
	}
}
