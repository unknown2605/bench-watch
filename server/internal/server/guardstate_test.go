package server

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/transcripts"
)

func TestGuardStateNameStartingAndUnknown(t *testing.T) {
	ctx := context.Background()
	st, code, err := guardStateName(ctx, nil, transcripts.GuardStatus{State: transcripts.GuardStarting})
	if err != nil || st != api.GuardResponseLogStateStarting || code != nil {
		t.Fatalf("starting: %q %v %v", st, code, err)
	}
	var buf bytes.Buffer
	log.SetOutput(&buf)
	warnedGuardStates.Delete("bogus-state")
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		warnedGuardStates.Delete("bogus-state")
	})
	for range 3 {
		st, code, err = guardStateName(ctx, nil, transcripts.GuardStatus{State: "bogus-state"})
		if err != nil || st != api.GuardResponseLogStateUnreadable || code == nil || *code != "EUNKNOWN" {
			t.Fatalf("unknown: %q %v %v", st, code, err)
		}
	}
	if n := strings.Count(buf.String(), "bogus-state"); n != 1 {
		t.Fatalf("logged %d times: %q", n, buf.String())
	}
}

func TestStaleNotRegularAndEmptyLogStateThroughTheAPI(t *testing.T) {
	pool := testPool(t)
	emptyFixtures(t, pool)
	t.Cleanup(func() { emptyFixtures(t, pool) })
	now := time.Date(2026, 11, 16, 15, 30, 0, 0, time.Local)
	srv, h := guardAPI(t, pool, now)
	get := func() api.GuardResponse { return decode[api.GuardResponse](t, h, "/api/guard?range=7d") }

	// A read log with no calls stores a state row but no figures: absent of figures, so not missing.
	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "guard.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tick(t, srv, empty, pool)
	tick(t, srv, filepath.Join(t.TempDir(), "absent"), pool)
	if out := get(); out.Log.State != api.GuardResponseLogStateAbsent {
		t.Fatalf("no figures stored, state %q", out.Log.State)
	}

	if _, err := pool.Exec(context.Background(), "UPDATE guard_log_state SET not_regular = 3"); err != nil {
		t.Fatal(err)
	}
	if out := get(); out.Log.Skipped.NotRegular != 0 {
		t.Fatalf("stale not_regular served in state %q: %d", out.Log.State, out.Log.Skipped.NotRegular)
	}
}
