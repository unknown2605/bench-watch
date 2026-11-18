package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/collect"
	"benchwatch/server/internal/collect/docker"
	"benchwatch/server/internal/collect/docker/dockertest"
	"benchwatch/server/internal/collect/runner"
	"benchwatch/server/internal/guard"
	"benchwatch/server/internal/transcripts"
)

func TestGuardMissingOnlyWhenNothingExistsAnywhere(t *testing.T) {
	none := guard.Install{Source: "none", Bash: "unknown", File: "unknown"}
	folderOnly := guard.Install{Source: "user", Bash: "unknown", File: "unknown"}
	modes := guard.Install{Source: "system", Bash: "go", File: "go"}
	build := guard.Install{Source: "user", Bash: "unknown", File: "unknown", Build: "0123456789ab"}
	has := func(v bool, err error) func() (bool, error) { return func() (bool, error) { return v, err } }
	boom := errors.New("db down")
	for _, tc := range []struct {
		name   string
		in     guard.Install
		state  string
		stored func() (bool, error)
		want   bool
	}{
		{"nothing installed, no collector", none, transcripts.GuardDisabled, nil, true},
		{"hooks folder without a guard", folderOnly, transcripts.GuardDisabled, nil, true},
		{"installed by modes", modes, transcripts.GuardDisabled, nil, false},
		{"installed by build", build, transcripts.GuardUnavailable, has(false, nil), false},
		{"log unavailable, nothing stored", none, transcripts.GuardUnavailable, has(false, nil), true},
		{"log gone but figures stored", none, transcripts.GuardUnavailable, has(true, nil), false},
		{"log unavailable, lookup fails", none, transcripts.GuardUnavailable, has(false, boom), false},
		{"log read ok", none, transcripts.GuardOK, nil, false},
		{"log failing", none, transcripts.GuardFailed, nil, false},
		{"log starting", none, transcripts.GuardStarting, nil, false},
	} {
		if got := guardMissing(tc.in, tc.state, tc.stored); got != tc.want {
			t.Errorf("%s: missing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStatusSaysDisabledForAbsentGuardAndNotForAFailingOne(t *testing.T) {
	home := t.TempDir()
	reader := &guard.InstallReader{SystemDir: filepath.Join(home, "none-system"), UserDir: filepath.Join(home, "hooks")}
	s := &Server{DB: allMigrations(), Runner: okRunner(), GuardInstall: reader}
	if st := status(t, s); st.Guard.State != api.CollectorStateDisabled {
		t.Errorf("no guard, no log: state %s, want disabled", st.Guard.State)
	}

	g := &transcripts.Guard{}
	g.Tick(context.Background())
	s.Guard = g
	if st := status(t, s); st.Guard.State != api.CollectorStateFailed || st.Guard.ErrorCode == nil {
		t.Errorf("guard log failing: state %s, want failed with a code", st.Guard.State)
	}

	s.Guard = &transcripts.Guard{}
	if st := status(t, s); st.Guard.State != api.CollectorStateStarting {
		t.Errorf("guard starting: state %s", st.Guard.State)
	}

	hooks := filepath.Join(home, "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	if st := status(t, &Server{DB: allMigrations(), Runner: okRunner(), GuardInstall: &guard.InstallReader{
		SystemDir: filepath.Join(home, "none-system"), UserDir: hooks}}); st.Guard.State != api.CollectorStateDisabled {
		t.Errorf("hooks folder only: state %s, want disabled", st.Guard.State)
	}
}

func TestStatusDockerAbsentIsDisabledAndStoppedIsNot(t *testing.T) {
	r := okRunner()
	r.Status.Fail(collect.SourceDocker, collect.StateUnavailable, errors.New("x"))
	if st := status(t, &Server{DB: allMigrations(), Runner: r}); st.Docker.State != api.CollectorStateUnavailable {
		t.Errorf("stopped daemon: state %s, want unavailable", st.Docker.State)
	}
	// No collector (no socket found at start) and nothing reported.
	if st := status(t, &Server{DB: allMigrations(), Runner: runner.New(runner.Options{}, nil, nil, nil, nil)}); st.Docker.State != api.CollectorStateDisabled {
		t.Errorf("no socket: state %s, want disabled", st.Docker.State)
	}
}

func TestStatusDockerFollowsTheSocketWithoutARestart(t *testing.T) {
	path := dockertest.ShortSocket(t)
	resolve := func() (string, docker.Presence) {
		if _, err := os.Lstat(path); err != nil {
			return "", docker.Absent
		}
		return "unix://" + path, docker.Found
	}
	dc, err := docker.New(docker.Options{Resolve: resolve})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	r := runner.New(runner.Options{}, nil, dc, nil, nil)
	s := &Server{DB: allMigrations(), Runner: r}
	want := func(step string, state api.CollectorState) {
		t.Helper()
		if st := status(t, s); st.Docker.State != state {
			t.Errorf("%s: docker %s, want %s", step, st.Docker.State, state)
		}
	}
	want("no socket at start", api.CollectorStateDisabled)

	stop := dockertest.StartSilent(t, path)
	r.Status.Fail(collect.SourceDocker, collect.StateUnavailable, errors.New("x"))
	want("socket appeared, daemon not answering", api.CollectorStateUnavailable)
	r.Status.OK(collect.SourceDocker)
	want("daemon answers", api.CollectorStateOk)

	stop()
	_ = os.Remove(path)
	want("socket gone again", api.CollectorStateDisabled)
}

func TestStoppedDockerDoesNotDegradeHealthButARunningFaultDoes(t *testing.T) {
	nominal := api.StatusResponse{Database: api.DatabaseStatus{State: api.DatabaseStateOk},
		Machine: api.CollectorStatus{State: api.CollectorStateOk},
		Guard:   api.CollectorStatus{State: api.CollectorStateDisabled}, Scan: api.ScanStatus{State: api.ScanStateOk}}
	unavailable := collect.SourceStatus{State: collect.StateUnavailable}
	s := &Server{}
	for name, tc := range map[string]struct {
		st              collect.SourceStatus
		absent, stopped bool
		state           api.CollectorState
		degraded        bool
	}{
		"installed, not running":    {unavailable, false, true, api.CollectorStateUnavailable, false},
		"installed, never polled":   {collect.SourceStatus{}, false, true, api.CollectorStateUnavailable, false},
		"running, collector errors": {unavailable, false, false, api.CollectorStateUnavailable, true},
		"absent":                    {unavailable, true, false, api.CollectorStateDisabled, false},
	} {
		out := nominal
		var partial bool
		out.Docker, partial = dockerView(tc.st, time.Second, false, tc.absent, tc.stopped)
		if out.Docker.State != tc.state || s.degraded(out, partial, tc.stopped) != tc.degraded {
			t.Errorf("%s: state %s degraded %v, want %s %v", name, out.Docker.State,
				s.degraded(out, partial, tc.stopped), tc.state, tc.degraded)
		}
	}
}

func TestCollectorStoppedMeansResolvedButNoSocketFile(t *testing.T) {
	path := dockertest.ShortSocket(t)
	presence := docker.Found
	dc, err := docker.New(docker.Options{Resolve: func() (string, docker.Presence) { return "unix://" + path, presence }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	if !dc.Stopped() || dc.Absent() {
		t.Errorf("no socket file: stopped %v absent %v, want true false", dc.Stopped(), dc.Absent())
	}
	stop := dockertest.StartSilent(t, path)
	defer stop()
	if dc.Stopped() {
		t.Error("socket file exists but Docker reads stopped")
	}
	presence = docker.Absent
	if dc.Stopped() {
		t.Error("absent Docker reads stopped")
	}
}

func TestCollectorStoppedFollowsSymlinkToTheSocket(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.sock")
	link := filepath.Join(dir, "docker.sock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	dc, err := docker.New(docker.Options{Resolve: func() (string, docker.Presence) { return "unix://" + link, docker.Found }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dc.Close() })
	if !dc.Stopped() {
		t.Error("symlink to a missing socket reads running")
	}
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if dc.Stopped() {
		t.Error("symlink to an existing file reads stopped")
	}
}
