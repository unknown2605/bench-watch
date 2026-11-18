package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

const livePath = "/api/live"

// paramCodes maps a parameter that failed the spec to its ErrorResponse code; any other parameter is bad_request.
var paramCodes = map[string]ErrorResponseCode{
	"range":   ErrorResponseCodeBadRange,
	"from":    ErrorResponseCodeBadDate,
	"to":      ErrorResponseCodeBadDate,
	"group":   ErrorResponseCodeBadGroup,
	"project": ErrorResponseCodeBadLabel,
	"session": ErrorResponseCodeBadLabel,
	"stack":   ErrorResponseCodeBadLabel,
}

// HandlerOptions carry what the server adds around the generated handler.
type HandlerOptions struct {
	// OnError hears every handler failure answered 500, with the spec's route template, never the request's values.
	OnError func(route string, err error)
	// MaxRequests caps the non-stream requests served at once; past it the answer is 503. Zero means 8.
	MaxRequests int
	// LiveWriteTimeout bounds each write to an event stream, so a client that stops reading is closed. Zero means 10 s.
	LiveWriteTimeout time.Duration
}

type codedError string

func (e codedError) Error() string { return string(e) }
func (e codedError) Code() string  { return string(e) }

// ErrBadResponse is a handler answer that breaks the spec, so the client got 500 instead.
const ErrBadResponse = codedError("ERESPONSE")

// NewHandler is the only way server code should serve the API: requests and responses are checked against the
// embedded spec, and every failure is answered with an ErrorResponse code alone, never error text.
func NewHandler(ssi StrictServerInterface) (http.Handler, error) {
	return NewHandlerWith(ssi, HandlerOptions{})
}

func NewHandlerWith(ssi StrictServerInterface, opt HandlerOptions) (http.Handler, error) {
	if opt.OnError == nil {
		opt.OnError = func(string, error) {}
	}
	if opt.MaxRequests <= 0 {
		opt.MaxRequests = 8
	}
	if opt.LiveWriteTimeout <= 0 {
		opt.LiveWriteTimeout = 10 * time.Second
	}
	spec, err := GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("embedded spec: %w", err)
	}
	// Requests arrive through the nginx proxy under its own host name, so routes match on the path only.
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}
	strict := NewStrictHandlerWithOptions(ssi, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, _ error) {
			WriteError(w, http.StatusBadRequest, ErrorResponseCodeBadRequest)
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			switch w := w.(type) {
			case *liveWriter:
				// A stream ends this way when its client goes; its status line is already sent.
				return
			case *recorder:
				w.err = err
			}
			WriteError(w, http.StatusInternalServerError, ErrorResponseCodeInternal)
		},
	})
	// Any path the spec router matched but the mux does not answers 404 here, never with the mux's own text.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		WriteError(w, http.StatusNotFound, ErrorResponseCodeNotFound)
	})
	inner := HandlerWithOptions(strict, StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			WriteError(w, http.StatusBadRequest, codeForParam(paramOf(err)))
		},
	})
	return &validating{router: router, inner: inner, opt: opt, slots: make(chan struct{}, opt.MaxRequests)}, nil
}

type validating struct {
	router routers.Router
	inner  http.Handler
	opt    HandlerOptions
	slots  chan struct{}
}

func (v *validating) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		WriteError(w, http.StatusMethodNotAllowed, ErrorResponseCodeBadMethod)
		return
	}
	// The spec router and ServeMux disagree on unclean paths (`/api/meta/`, `//`, `.`), so only clean ones route.
	if r.URL.Path != path.Clean(r.URL.Path) {
		WriteError(w, http.StatusNotFound, ErrorResponseCodeNotFound)
		return
	}
	route, pathParams, err := v.router.FindRoute(r)
	if err != nil {
		WriteError(w, http.StatusNotFound, ErrorResponseCodeNotFound)
		return
	}
	in := &openapi3filter.RequestValidationInput{
		Request:    r,
		PathParams: pathParams,
		Route:      route,
		// Filling in the spec defaults would add range=30d beside from and to, which the period rules refuse.
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
			SkipSettingDefaults: true},
	}
	if err := openapi3filter.ValidateRequest(r.Context(), in); err != nil {
		var reqErr *openapi3filter.RequestError
		name := ""
		if errors.As(err, &reqErr) && reqErr.Parameter != nil {
			name = reqErr.Parameter.Name
		}
		WriteError(w, http.StatusBadRequest, codeForParam(name))
		return
	}
	if route.Path == livePath {
		lw := &liveWriter{ResponseWriter: w, rc: http.NewResponseController(w), timeout: v.opt.LiveWriteTimeout}
		defer lw.finish()
		v.inner.ServeHTTP(lw, r.WithContext(context.WithValue(r.Context(), abortKey{}, lw.abort)))
		return
	}
	rec, ok := v.answer(route.Path, in, r)
	// A request that ran past its read deadline has a cancelled connection context, which keep-alive would pass on.
	if r.Context().Err() != nil {
		w.Header().Set("Connection", "close")
	}
	switch {
	case !ok:
		WriteError(w, http.StatusServiceUnavailable, ErrorResponseCodeUnavailable)
		return
	case rec == nil:
		WriteError(w, http.StatusInternalServerError, ErrorResponseCodeInternal)
		return
	}
	for k, vals := range rec.header {
		w.Header()[k] = vals
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.body.Bytes())
}

// answer holds a request slot only while the handler and the response check run, so a client that reads its answer
// slowly holds its connection, never a slot; false means every slot is taken, and a nil answer broke the spec.
func (v *validating) answer(route string, in *openapi3filter.RequestValidationInput, r *http.Request) (*recorder,
	bool) {
	select {
	case v.slots <- struct{}{}:
		defer func() { <-v.slots }()
	default:
		return nil, false
	}
	rec := &recorder{header: http.Header{}, status: http.StatusOK}
	v.inner.ServeHTTP(rec, r)
	if rec.err != nil {
		v.opt.OnError(route, rec.err)
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in,
		Status:                 rec.status,
		Header:                 rec.header,
		Body:                   io.NopCloser(bytes.NewReader(rec.body.Bytes())),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(r.Context(), out); err != nil {
		v.opt.OnError(route, ErrBadResponse)
		return nil, true
	}
	return rec, true
}

// Admit refuses a GET or HEAD that carries a body, and any other request with one, then closes the connection;
// it gives every request but the event stream a read deadline, so a drip-fed body can hold neither a slot nor a socket.
func Admit(w http.ResponseWriter, r *http.Request, readTimeout time.Duration) bool {
	rc := http.NewResponseController(w)
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		w.Header().Set("Connection", "close")
		// net/http drains up to 256 KiB of an unread body after the answer; an expired deadline ends that at once.
		_ = rc.SetReadDeadline(time.Now())
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			WriteError(w, http.StatusBadRequest, ErrorResponseCodeBadRequest)
		} else {
			w.Header().Set("Allow", http.MethodGet)
			WriteError(w, http.StatusMethodNotAllowed, ErrorResponseCodeBadMethod)
		}
		return false
	}
	if r.URL.Path != livePath {
		_ = rc.SetReadDeadline(time.Now().Add(readTimeout))
	}
	return true
}

func codeForParam(name string) ErrorResponseCode {
	if code, ok := paramCodes[name]; ok {
		return code
	}
	return ErrorResponseCodeBadRequest
}

func paramOf(err error) string {
	var (
		format   *InvalidParamFormatError
		required *RequiredParamError
		unescape *UnescapedCookieParamError
		header   *RequiredHeaderError
		many     *TooManyValuesForParamError
		unmarsh  *UnmarshalingParamError
	)
	switch {
	case errors.As(err, &format):
		return format.ParamName
	case errors.As(err, &required):
		return required.ParamName
	case errors.As(err, &unescape):
		return unescape.ParamName
	case errors.As(err, &header):
		return header.ParamName
	case errors.As(err, &many):
		return many.ParamName
	case errors.As(err, &unmarsh):
		return unmarsh.ParamName
	}
	return ""
}

func WriteError(w http.ResponseWriter, status int, code ErrorResponseCode) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Code: code})
}

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
	wrote  bool
	err    error
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if !r.wrote {
		r.status = status
		r.wrote = true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}

type abortKey struct{}

// LiveAbort returns the function that ends the event stream of the request behind ctx, or a no-op elsewhere.
func LiveAbort(ctx context.Context) func() {
	if f, ok := ctx.Value(abortKey{}).(func()); ok {
		return f
	}
	return func() {}
}

var errAborted = errors.New("stream aborted")

// liveWriter gives every stream write a deadline, so a client that stops reading cannot hold the handler forever.
type liveWriter struct {
	http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration

	mu      sync.Mutex
	aborted bool
	done    bool
}

func (l *liveWriter) Unwrap() http.ResponseWriter { return l.ResponseWriter }

func (l *liveWriter) arm() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.aborted {
		_ = l.rc.SetWriteDeadline(time.Now().Add(l.timeout))
	}
	return !l.aborted
}

func (l *liveWriter) Write(b []byte) (int, error) {
	if !l.arm() {
		return 0, errAborted
	}
	return l.ResponseWriter.Write(b)
}

func (l *liveWriter) Flush() {
	if l.arm() {
		_ = l.rc.Flush()
	}
}

// abort expires the write deadline at once, which also frees a write already blocked on the socket.
func (l *liveWriter) abort() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done && !l.aborted {
		l.aborted = true
		_ = l.rc.SetWriteDeadline(time.Unix(1, 0))
	}
}

// finish stops abort from touching the connection, which keep-alive may hand to another request.
func (l *liveWriter) finish() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.done = true
}

// LiveEncoder turns a LiveEvent into one Server-Sent Events frame after checking it against the spec, since
// the stream is not buffered and validated like the other responses.
type LiveEncoder struct {
	schema *openapi3.Schema
}

func NewLiveEncoder() (*LiveEncoder, error) {
	spec, err := GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("embedded spec: %w", err)
	}
	ref := spec.Components.Schemas["LiveEvent"]
	if ref == nil || ref.Value == nil {
		return nil, errors.New("embedded spec has no LiveEvent schema")
	}
	return &LiveEncoder{schema: ref.Value}, nil
}

// Frame returns `event: <type>` and `data: <json>` lines, or an error and no bytes when the event breaks the spec.
func (e *LiveEncoder) Frame(_ context.Context, ev LiveEvent) ([]byte, error) {
	data, err := json.Marshal(ev)
	if err != nil {
		return nil, errors.New("live event does not encode")
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, errors.New("live event does not decode")
	}
	if err := e.schema.VisitJSON(decoded); err != nil {
		return nil, errors.New("live event breaks the spec")
	}
	kind, _ := decoded.(map[string]any)["type"].(string)
	if kind == "" || strings.ContainsAny(kind, "\r\n") {
		return nil, errors.New("live event has no type")
	}
	return []byte("event: " + kind + "\ndata: " + string(data) + "\n\n"), nil
}
