package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type deadlineWriter struct {
	*httptest.ResponseRecorder
	read []time.Time
}

func (w *deadlineWriter) SetReadDeadline(t time.Time) error {
	w.read = append(w.read, t)
	return nil
}

// A read deadline on the event stream would end it: the stream reads nothing after its request.
func TestAdmitGivesEveryRequestButTheStreamAReadDeadline(t *testing.T) {
	const timeout = time.Minute
	for target, deadlines := range map[string]int{"/api/live": 0, "/api/live/": 1, "/api/live2": 1, "/api/status": 1,
		"/api/machine?range=1h": 1, "/": 1} {
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		start := time.Now()
		if !Admit(w, httptest.NewRequest(http.MethodGet, target, nil), timeout) {
			t.Errorf("%s: refused", target)
		}
		if len(w.read) != deadlines {
			t.Errorf("%s: %d read deadlines set, want %d", target, len(w.read), deadlines)
			continue
		}
		for _, d := range w.read {
			if d.Before(start.Add(timeout)) || d.After(time.Now().Add(timeout)) {
				t.Errorf("%s: read deadline %v after the request, want %v", target, d.Sub(start), timeout)
			}
		}
	}
	// A refused body gets an expired deadline on every path, so net/http cannot wait for the rest of it.
	for _, target := range []string{"/api/live", "/api/status"} {
		w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		if Admit(w, httptest.NewRequest(http.MethodGet, target, strings.NewReader("x")), timeout) {
			t.Errorf("%s with a body: admitted", target)
		}
		if len(w.read) != 1 || w.read[0].After(time.Now()) || w.Code != http.StatusBadRequest ||
			w.Header().Get("Connection") != "close" {
			t.Errorf("%s with a body: deadlines %v, status %d, Connection %q; want one expired deadline, 400, close",
				target, w.read, w.Code, w.Header().Get("Connection"))
		}
	}
}
