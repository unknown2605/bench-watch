package docker

import (
	"net/http"
	"regexp"
	"strings"
)

// readPaths is every endpoint the collector may call, matched after the /vX.Y prefix.
var readPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/_ping$`),
	regexp.MustCompile(`^/containers/json$`),
	regexp.MustCompile(`^/containers/[A-Za-z0-9][A-Za-z0-9_.-]*/stats$`),
	regexp.MustCompile(`^/events$`),
	regexp.MustCompile(`^/system/df$`),
	regexp.MustCompile(`^/info$`),
}

var versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+`)

type guardError struct{}

func (guardError) Error() string { return "docker request outside the read-only list" }
func (guardError) Code() string  { return "EGUARD" }

// ErrGuard is returned for any request that is not a GET or HEAD to a listed read endpoint.
var ErrGuard error = guardError{}

// readOnly is a request hook: the client refuses the request before it reaches the socket.
func readOnly(req *http.Request) error {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return ErrGuard
	}
	p := versionPrefix.ReplaceAllString(req.URL.Path, "")
	if strings.Contains(p, "..") {
		return ErrGuard
	}
	for _, re := range readPaths {
		if re.MatchString(p) {
			return nil
		}
	}
	return ErrGuard
}
