package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/labels"
)

// Settings unreadable at start keep the defaults only until a read succeeds with other values.
func TestSettingsWatchEndsTheProcessWhenIntervalsDiffer(t *testing.T) {
	inUse := runner.New(runner.Options{}, nil, nil, nil, nil).Options()
	reads := []struct {
		hist, apps time.Duration
		err        error
	}{{0, 0, errors.New("down")}, {inUse.Hist, inUse.Apps, nil}, {30 * time.Second, time.Minute, nil}}
	n := 0
	read := func(context.Context) (time.Duration, time.Duration, error) {
		r := reads[min(n, len(reads)-1)]
		n++
		return r.hist, r.apps, r.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := watchSettings(ctx, read, inUse, time.Millisecond); !errors.Is(err, errSettings) || n != 3 {
		t.Fatalf("watch returned %v after %d reads, want ESETTINGS after 3", err, n)
	}
	if code(errSettings) != "ESETTINGS" {
		t.Errorf("exit code %q", code(errSettings))
	}
}

func TestSettingsWatchKeepsRunningOnSameIntervals(t *testing.T) {
	inUse := runner.New(runner.Options{Hist: 10 * time.Second, Apps: 60 * time.Second}, nil, nil, nil, nil).Options()
	read := func(context.Context) (time.Duration, time.Duration, error) { return inUse.Hist, inUse.Apps, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := watchSettings(ctx, read, inUse, time.Millisecond); err != nil {
		t.Fatalf("watch with unchanged settings returned %v", err)
	}
}

// A start with the database down uses the defaults, which equal the migrated 30 s settings, so no restart follows.
func TestSettingsWatchKeepsDefaultsThatMatchTheMigratedSettings(t *testing.T) {
	inUse := runner.New(runner.Options{}, nil, nil, nil, nil).Options()
	if inUse.Hist != 30*time.Second || inUse.Apps != 30*time.Second || inUse.Hist%inUse.Live != 0 {
		t.Fatalf("defaults hist %v apps %v live %v, want 30 s, 30 s and a multiple of live", inUse.Hist, inUse.Apps,
			inUse.Live)
	}
	read := func(context.Context) (time.Duration, time.Duration, error) {
		return 30 * time.Second, 30 * time.Second, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := watchSettings(ctx, read, inUse, time.Millisecond); err != nil {
		t.Fatalf("watch with the migrated settings returned %v", err)
	}
}

func TestWaitAtMostGivesUp(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Done()
	start := time.Now()
	if waitAtMost(&wg, 50*time.Millisecond) || time.Since(start) > 5*time.Second {
		t.Fatal("waitAtMost waited for a stuck loop")
	}
}

// CM-00010 QA: the agent built its gate from labels.LooksSecret alone, so the collector's letter-pair, digit and
// word rules never ran in the binary. Each refused name here passes that old gate.
func TestNameGateIsTheCollectorDefault(t *testing.T) {
	gate, old := nameGate(), collect.NewNameGate(labels.LooksSecret)
	for _, s := range []string{"qzxvkjwpfhtm", "session-qzxvkjwpfhtm", "4111-1111-1111-1111", "555.123.4567"} {
		if !old(s) {
			t.Errorf("%q does not tell the gates apart: the label check alone refuses it", s)
		}
		if gate(s) {
			t.Errorf("the agent's gate shows %q", s)
		}
	}
	for _, s := range []string{"Claude Helper (Renderer)", "AuthenticationServicesAgent", "mysqldump", "node"} {
		if !gate(s) {
			t.Errorf("the agent's gate hides %q", s)
		}
	}
}

func TestDockerOptionsChoice(t *testing.T) {
	env := func(host string, exists bool) docker.Env {
		return docker.Env{Getenv: func(k string) string {
			if k == "DOCKER_HOST" {
				return host
			}
			return ""
		}, GOOS: "linux", Exists: func(string) bool { return exists }}
	}
	if _, wanted := dockerOptions("", env("", true)); wanted {
		t.Error("an empty flag must skip Docker")
	}
	if o, wanted := dockerOptions("unix:///x.sock", env("tcp://1.2.3.4:1", false)); !wanted || o.Host != "unix:///x.sock" ||
		o.Resolve != nil {
		t.Errorf("explicit flag: %+v", o)
	}
	for _, tc := range []struct {
		name string
		env  docker.Env
		want docker.Presence
		host string
	}{
		{"no socket yet", env("", false), docker.Absent, ""},
		{"default socket", env("", true), docker.Found, "unix:///var/run/docker.sock"},
		{"a tcp DOCKER_HOST is never dialled", env("tcp://1.2.3.4:2375", true), docker.Unsupported, ""},
	} {
		o, wanted := dockerOptions(dockerAuto, tc.env)
		if !wanted || o.Resolve == nil || o.Host != "" {
			t.Fatalf("%s: %+v", tc.name, o)
		}
		if host, got := o.Resolve(); host != tc.host || got != tc.want {
			t.Errorf("%s: %q, %v", tc.name, host, got)
		}
	}
}
