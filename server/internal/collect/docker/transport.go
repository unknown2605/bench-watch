package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/moby/moby/client"
	"go.opentelemetry.io/otel/trace/noop"
)

// Limits per endpoint: a real list of 21 containers is 80 KB and 2211 JSON nodes, a legacy disk report 400 KB and
// 11786, 5.6 times under its cap (the main disk call lists no items; the second lists images only). Peak: 21 MiB.
var (
	listCap   = bodyCap{bytes: 8 << 20, nodes: 1 << 16}
	diskCap   = bodyCap{bytes: 16 << 20, nodes: 1 << 16}
	infoCap   = bodyCap{bytes: 1 << 20, nodes: 1 << 13}
	pingCap   = bodyCap{bytes: 64 << 10, nodes: 1 << 10}
	statsCap  = bodyCap{bytes: 1 << 20, nodes: 1 << 12, perValue: true}
	eventsCap = bodyCap{bytes: 64 << 10, perValue: true}
)

// bodyCap: bytes in all, or per top-level JSON value when perValue; nodes counts '{', '[' and ',' outside strings.
type bodyCap struct {
	bytes, nodes int64
	perValue     bool
}

const recordSeparator = 0x1e

type tooLargeError struct{ limit int64 }

func (e tooLargeError) Error() string {
	return fmt.Sprintf("docker response over %d bytes or nodes", e.limit)
}
func (tooLargeError) Code() string { return "ETOOLARGE" }

type tripKey struct{}

// trip records a capped body that hit its limit, for calls whose client error keeps only the text (info, df).
type trip struct{ limit atomic.Int64 }

func withTrip(ctx context.Context) (context.Context, *trip) {
	t := &trip{}
	return context.WithValue(ctx, tripKey{}, t), t
}

func (t *trip) err(err error) error {
	if l := t.limit.Load(); err != nil && l > 0 {
		return tooLargeError{l}
	}
	return err
}

func bodyLimit(path string) bodyCap {
	switch {
	case strings.HasSuffix(path, "/events"):
		return eventsCap
	case strings.HasSuffix(path, "/containers/json"):
		return listCap
	case strings.HasSuffix(path, "/system/df"):
		return diskCap
	case strings.HasSuffix(path, "/_ping"):
		return pingCap
	case strings.HasSuffix(path, "/stats"):
		return statsCap
	}
	return infoCap
}

type limitTransport struct{ base http.RoundTripper }

func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err != nil || res.Body == nil {
		return res, err
	}
	tr, _ := req.Context().Value(tripKey{}).(*trip)
	res.Body = &cappedBody{rc: res.Body, cap: bodyLimit(req.URL.Path), trip: tr}
	return res, nil
}

// cappedBody follows the JSON nesting of what it reads, so a value cannot pass a cap by being split over lines.
type cappedBody struct {
	rc          io.ReadCloser
	cap         bodyCap
	trip        *trip
	used, nodes int64
	depth       int
	inStr, esc  bool
	failed      error
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.failed != nil {
		return 0, b.failed
	}
	n, err := b.rc.Read(p)
	for _, c := range p[:n] {
		if limit := b.step(c); limit > 0 {
			if b.trip != nil {
				b.trip.limit.Store(limit)
			}
			b.failed = tooLargeError{limit}
			return 0, b.failed
		}
	}
	return n, err
}

// step returns the limit c passed, or 0. The client drops record separators from a json-seq stream before
// decoding and any other decoder fails on one, so 0x1e never moves the JSON state.
func (b *cappedBody) step(c byte) int64 {
	if b.used++; b.used > b.cap.bytes {
		return b.cap.bytes
	}
	if c == recordSeparator {
		return 0
	}
	if b.inStr {
		switch {
		case b.esc:
			b.esc = false
		case c == '\\':
			b.esc = true
		case c == '"':
			b.inStr = false
			b.valueEnd()
		}
		return 0
	}
	switch c {
	case '"':
		b.inStr = true
	case '{', '[':
		b.depth++
		b.nodes++
	case '}', ']':
		b.depth = max(b.depth-1, 0)
		b.valueEnd()
	case ',':
		b.nodes++
	}
	if b.cap.nodes > 0 && b.nodes > b.cap.nodes {
		return b.cap.nodes
	}
	return 0
}

func (b *cappedBody) valueEnd() {
	if b.cap.perValue && b.depth == 0 {
		b.used, b.nodes = 0, 0
	}
}

func (b *cappedBody) Close() error { return b.rc.Close() }

// maxHeaderBytes: a daemon's headers are under 1 KB; Go's default would buffer 10 MiB.
const maxHeaderBytes = 64 << 10

// loopback: tcp:// carries no TLS here, so it may only reach this machine.
func loopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newClient builds the HTTP transport itself so every response body is capped; it returns the transport
// too, because the client closes idle connections only of a transport it built.
func newClient(host string) (*client.Client, *http.Transport, error) { return newClientPick(host, nil) }

// newClientPick dials the path pick returns on every connection instead of the one in host, when pick is set.
func newClientPick(host string, pick func() (string, error)) (*client.Client, *http.Transport, error) {
	u, err := client.ParseHostURL(host)
	if err != nil {
		return nil, nil, err
	}
	tr := &http.Transport{MaxIdleConns: 6, IdleConnTimeout: 30 * time.Second, DisableCompression: true,
		MaxResponseHeaderBytes: maxHeaderBytes}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	switch u.Scheme {
	case "unix":
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			path := u.Host
			if pick != nil {
				var err error
				if path, err = pick(); err != nil {
					return nil, err
				}
			}
			return dialer.DialContext(ctx, "unix", path)
		}
	case "tcp", "http":
		if !loopback(u.Host) {
			return nil, nil, fmt.Errorf("docker host over plain HTTP must be on this machine")
		}
		tr.DialContext = dialer.DialContext
	default:
		return nil, nil, fmt.Errorf("unsupported docker host scheme %q", u.Scheme)
	}
	cli, err := client.New(
		client.WithHost(host),
		client.WithHTTPClient(&http.Client{Transport: limitTransport{tr}, CheckRedirect: client.CheckRedirect}),
		client.WithHTTPRequestHook(readOnly),
		client.WithTraceProvider(noop.NewTracerProvider()),
		client.WithUserAgent("benchwatch"),
	)
	if err != nil {
		return nil, nil, err
	}
	return cli, tr, nil
}
