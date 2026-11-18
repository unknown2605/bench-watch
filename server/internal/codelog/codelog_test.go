package codelog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	flags := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	return &buf
}

func TestChangesLogOnlyTransitions(t *testing.T) {
	buf := capture(t)
	c := &Changes{}
	for _, code := range []string{"", "ETIMEDOUT", "ETIMEDOUT", "", "", "/Users/x/secret"} {
		c.Note("scan write", code)
	}
	want := "scan write: ETIMEDOUT\nscan write: ok\nscan write: EOTHER\n"
	if buf.String() != want {
		t.Errorf("log %q, want %q", buf.String(), want)
	}
}

func TestLimiterLogsOncePerInterval(t *testing.T) {
	buf := capture(t)
	now := time.Unix(1000, 0)
	l := &Limiter{Every: time.Minute, Now: func() time.Time { return now }}
	l.Print("api /api/meta", "EPG_57P01")
	l.Print("api /api/meta", "EPG_57P01")
	now = now.Add(time.Minute)
	l.Print("api /api/meta", "EPG_57P01")
	if got := strings.Count(buf.String(), "api /api/meta: EPG_57P01"); got != 2 {
		t.Errorf("%d lines, want 2: %q", got, buf.String())
	}
}

type coded struct{}

func (coded) Error() string { return "text with /a/path" }
func (coded) Code() string  { return "ERESPONSE" }

func TestOfNamesErrorsByCode(t *testing.T) {
	for err, want := range map[error]string{
		nil: "", context.DeadlineExceeded: "ETIMEDOUT", fmt.Errorf("x: %w", syscall.ECONNREFUSED): "ECONNREFUSED",
		&pgconn.PgError{Code: "57P01"}: "EPG_57P01", coded{}: "ERESPONSE", errors.New("/Users/x"): "EOTHER",
	} {
		if got := Of(err); got != want {
			t.Errorf("Of(%T) = %q, want %q", err, got, want)
		}
	}
}
