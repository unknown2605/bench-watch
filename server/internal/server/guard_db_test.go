package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/guard"
)

func TestGuardTrendSumsBlockedRowsPerDay(t *testing.T) {
	pool := testPool(t)
	wipe := func() {
		for _, table := range []string{"tool_error_daily", "tool_result_daily"} {
			if _, err := pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
				t.Fatalf("empty %s: %v", table, err)
			}
		}
	}
	emptyFixtures(t, pool)
	wipe()
	t.Cleanup(func() { wipe(); emptyFixtures(t, pool) })
	day := func(ago int) string { return time.Now().AddDate(0, 0, -ago).Format(time.DateOnly) }
	insert := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []struct {
		ago      int
		proj, tl string
		n        int
	}{{3, "alpha", "Read: whole", 5}, {3, "beta", "Bash: git", 7}, {5, "alpha", "Read: whole", 4}} {
		insert("INSERT INTO tool_result_daily VALUES ($1::date, $2, 'main', $3, $4, 0, 0, 0, 0, 0)",
			day(r.ago), r.proj, r.tl, r.n)
	}
	for _, e := range []struct {
		ago         int
		proj, tl, c string
		n           int
	}{{3, "alpha", "Read: whole", "blocked by hook", 1}, {3, "beta", "Bash: git", "blocked by hook", 2},
		{3, "beta", "Bash: git", "denied", 4}, {2, "alpha", "Read: whole", "blocked by hook", 3}} {
		insert("INSERT INTO tool_error_daily VALUES ($1::date, $2, 'main', $3, $4, $5)",
			day(e.ago), e.proj, e.tl, e.c, e.n)
	}
	srv, h := newServer(t, pool, memoryRunner(time.Now(), 5, 2*time.Second))

	type point struct {
		day              string
		results, blocked int64
	}
	read := func(path string) []point {
		var out []point
		for _, p := range decode[api.GuardResponse](t, h, path).Trend {
			out = append(out, point{p.Day.Format(time.DateOnly), p.Results, p.Blocked})
		}
		return out
	}
	want := []point{{day(5), 4, 0}, {day(3), 12, 3}, {day(2), 0, 3}}
	if got := read("/api/guard?range=30d"); !equalPoints(got, want) {
		t.Fatalf("trend %v, want %v (day -4 absent, -2 has blocked rows only)", got, want)
	}
	if got := read("/api/guard?range=30d&project=alpha"); !equalPoints(got, []point{{day(5), 4, 0}, {day(3), 5, 1},
		{day(2), 0, 3}}) {
		t.Fatalf("trend for one project: %v", got)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "guard-mode"), []byte("bash=go\nfile=node\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.GuardInstall = &guard.InstallReader{SystemDir: filepath.Join(root, "absent"), UserDir: root}
	in := decode[api.GuardResponse](t, h, "/api/guard?range=7d").Install
	if in.Source != api.GuardResponseInstallSource("user") || in.Bash != api.GuardMode("go") || in.File != api.GuardMode("node") ||
		in.Build != nil || in.InstalledAt != nil {
		t.Fatalf("install %+v", in)
	}
}

func equalPoints[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
