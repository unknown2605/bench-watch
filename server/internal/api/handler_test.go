package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stub answers only the operations these tests call; any other call panics through the nil interface.
type stub struct {
	StrictServerInterface
	meta MetaResponse
}

func (s stub) GetMeta(context.Context, GetMetaRequestObject) (GetMetaResponseObject, error) {
	return GetMeta200JSONResponse(s.meta), nil
}

func (stub) GetStatus(context.Context, GetStatusRequestObject) (GetStatusResponseObject, error) {
	return nil, errors.New("dial tcp 127.0.0.1:5432: /Users/secret/path refused")
}

func validMeta() MetaResponse {
	return MetaResponse{
		GeneratedAt: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), Projects: []ProjectLabel{"benchwatch"},
		Models: []ModelLabel{}, AgentTypes: []AgentTypeLabel{}, Tools: []ToolLabel{"Read: image"},
		Stacks: []StackLabel{}, ContextBands: []TokenBand{}, ResultSizeBands: []TokenBand{}, RereadTarget: 0.15,
		Retention: []RetentionTier{}, Clients: []ClientInfo{{Id: ClaudeCode, Verified: true}},
	}
}

func serve(t *testing.T, s StrictServerInterface, method, target string) (int, string) {
	t.Helper()
	h, err := NewHandler(s)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec.Code, rec.Body.String()
}

func TestHandlerAnswersWithCodesOnly(t *testing.T) {
	leaky := validMeta()
	leaky.Projects = []ProjectLabel{"/Users/secret/project"}
	cases := []struct {
		name, method, target string
		s                    StrictServerInterface
		status               int
		code                 ErrorResponseCode
	}{
		{"unknown path", http.MethodGet, "/api/nope", stub{}, 404, ErrorResponseCodeNotFound},
		{"trailing slash", http.MethodGet, "/api/meta/", stub{meta: validMeta()}, 404, ErrorResponseCodeNotFound},
		{"double slash", http.MethodGet, "/api//meta", stub{meta: validMeta()}, 404, ErrorResponseCodeNotFound},
		{"dot segment", http.MethodGet, "/api/./meta", stub{meta: validMeta()}, 404, ErrorResponseCodeNotFound},
		{"dot-dot segment", http.MethodGet, "/api/x/../meta", stub{meta: validMeta()}, 404, ErrorResponseCodeNotFound},
		{"session with slash", http.MethodGet, "/api/sessions/0a1b2c3d/?project=a", stub{}, 404, ErrorResponseCodeNotFound},
		{"other method", http.MethodPost, "/api/status", stub{}, 405, ErrorResponseCodeBadMethod},
		{"bad preset", http.MethodGet, "/api/overview?range=3d", stub{}, 400, ErrorResponseCodeBadRange},
		{"bad date", http.MethodGet, "/api/overview?from=2026-13-01&to=2026-13-02", stub{}, 400, ErrorResponseCodeBadDate},
		{"path as project", http.MethodGet, "/api/overview?project=%2FUsers%2Fsecret", stub{}, 400, ErrorResponseCodeBadLabel},
		{"dot project", http.MethodGet, "/api/overview?project=.env", stub{}, 400, ErrorResponseCodeBadLabel},
		{"unknown client", http.MethodGet, "/api/overview?client=cursor", stub{}, 400, ErrorResponseCodeBadRequest},
		{"bad group", http.MethodGet, "/api/fills?group=secret", stub{}, 400, ErrorResponseCodeBadGroup},
		{"bad session", http.MethodGet, "/api/sessions/ZZZZZZZZ?project=a", stub{}, 400, ErrorResponseCodeBadLabel},
		{"bad stack", http.MethodGet, "/api/docker?stack=Has%20Space", stub{}, 400, ErrorResponseCodeBadLabel},
		{"handler error", http.MethodGet, "/api/status", stub{}, 500, ErrorResponseCodeInternal},
		{"response breaks the spec", http.MethodGet, "/api/meta", stub{meta: leaky}, 500, ErrorResponseCodeInternal},
	}
	for _, c := range cases {
		status, body := serve(t, c.s, c.method, c.target)
		var got map[string]any
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Errorf("%s: body is not JSON: %q", c.name, body)
			continue
		}
		if status != c.status || len(got) != 1 || got["code"] != string(c.code) {
			t.Errorf("%s: %d %s, want %d {code: %s}", c.name, status, body, c.status, c.code)
		}
		if strings.Contains(body, "secret") || strings.Contains(body, "/Users") {
			t.Errorf("%s: body leaks request or error text: %s", c.name, body)
		}
	}
}

func TestHandlerPassesValidResponses(t *testing.T) {
	status, body := serve(t, stub{meta: validMeta()}, http.MethodGet, "/api/meta")
	if status != 200 || !strings.Contains(body, `"Read: image"`) {
		t.Fatalf("%d %s", status, body)
	}
}

func TestLiveEncoderRefusesEventsOutsideTheSpec(t *testing.T) {
	enc, err := NewLiveEncoder()
	if err != nil {
		t.Fatal(err)
	}
	event := func(project string) LiveEvent {
		var ev LiveEvent
		raw := `{"type":"activity","data":{"kind":"scan_ok","at":"2026-11-08T12:00:00Z","project":"` + project + `"}}`
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	frame, err := enc.Frame(context.Background(), event("benchwatch"))
	if err != nil || !strings.HasPrefix(string(frame), "event: activity\ndata: {") || !strings.HasSuffix(string(frame), "}\n\n") {
		t.Fatalf("valid event: %q %v", frame, err)
	}
	if frame, err := enc.Frame(context.Background(), event("/Users/secret")); err == nil {
		t.Fatalf("event with a path accepted: %s", frame)
	}
}
