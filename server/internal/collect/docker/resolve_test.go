package docker

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func tempSocket(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

func TestResolve(t *testing.T) {
	// Unix socket paths are short-limited, so the fake home sits in a short temp folder.
	root, err := os.MkdirTemp("", "bw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, xdg := filepath.Join(root, "h"), filepath.Join(root, "x")
	homeSock, xdgSock := filepath.Join(home, ".docker", "run", "docker.sock"), filepath.Join(xdg, "docker.sock")
	tempSocket(t, homeSock)
	tempSocket(t, xdgSock)
	dangling := filepath.Join(root, "d", ".docker", "run", "docker.sock")
	if err := os.MkdirAll(filepath.Dir(dangling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "gone"), dangling); err != nil {
		t.Fatal(err)
	}
	system := func(p string) bool { return p != "/var/run/docker.sock" && OSEnv().Exists(p) }
	withSystem := func(p string) bool { return p == "/var/run/docker.sock" || OSEnv().Exists(p) }

	tests := []struct {
		name   string
		env    map[string]string
		goos   string
		home   string
		exists func(string) bool
		host   string
		want   Presence
	}{
		{"DOCKER_HOST unix wins", map[string]string{"DOCKER_HOST": "unix:///run/custom.sock"}, "darwin", home, system,
			"unix:///run/custom.sock", Found},
		{"DOCKER_HOST tcp is unsupported", map[string]string{"DOCKER_HOST": "tcp://127.0.0.1:2375"}, "linux", home,
			withSystem, "", Unsupported},
		{"DOCKER_HOST ssh is unsupported", map[string]string{"DOCKER_HOST": "ssh://user@host"}, "linux", home, withSystem,
			"", Unsupported},
		{"DOCKER_HOST relative unix is unsupported", map[string]string{"DOCKER_HOST": "unix://docker.sock"}, "linux", home,
			withSystem, "", Unsupported},
		{"mac user socket first", nil, "darwin", home, withSystem, "unix://" + homeSock, Found},
		{"mac falls back to system socket", nil, "darwin", filepath.Join(root, "none"), withSystem,
			"unix:///var/run/docker.sock", Found},
		{"mac stopped Docker Desktop link counts as present", nil, "darwin", filepath.Join(root, "d"), system,
			"unix://" + dangling, Found},
		{"mac none", nil, "darwin", filepath.Join(root, "none"), system, "", Absent},
		{"linux system socket first", nil, "linux", home, withSystem, "unix:///var/run/docker.sock", Found},
		{"linux rootless socket", map[string]string{"XDG_RUNTIME_DIR": xdg}, "linux", home, system,
			"unix://" + xdgSock, Found},
		{"linux no XDG dir", nil, "linux", home, system, "", Absent},
		{"linux relative XDG dir is ignored", map[string]string{"XDG_RUNTIME_DIR": "x"}, "linux", home, system, "", Absent},
		{"linux ignores the mac path", nil, "linux", home, system, "", Absent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := Env{Getenv: func(k string) string { return tc.env[k] }, GOOS: tc.goos, Home: tc.home, Exists: tc.exists}
			host, got := Resolve(e)
			if host != tc.host || got != tc.want {
				t.Fatalf("Resolve = %q, %v; want %q, %v", host, got, tc.host, tc.want)
			}
		})
	}
}

func TestResolveCountsTheInstalledDesktopAppAsPresent(t *testing.T) {
	home := "/nowhere/home"
	homeSock := filepath.Join(home, ".docker", "run", "docker.sock")
	none := func(string) bool { return false }
	appOnly := func(p string) bool { return p == DesktopApp }
	for _, tc := range []struct {
		name, goos, app, env string
		exists               func(string) bool
		host                 string
		want                 Presence
	}{
		{"mac, no socket, app installed", "darwin", DesktopApp, "", appOnly, "unix://" + homeSock, Found},
		{"mac, no socket, no app", "darwin", DesktopApp, "", none, "", Absent},
		{"mac, app check off", "darwin", "", "", appOnly, "", Absent},
		{"linux ignores the app", "linux", DesktopApp, "", appOnly, "", Absent},
		{"DOCKER_HOST wins over the app", "darwin", DesktopApp, "tcp://h:1", appOnly, "", Unsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Env{Getenv: func(k string) string {
				if k == "DOCKER_HOST" {
					return tc.env
				}
				return ""
			}, GOOS: tc.goos, Home: home, Exists: tc.exists, DockerApp: tc.app}
			host, got := Resolve(e)
			if host != tc.host || got != tc.want {
				t.Fatalf("Resolve = %q, %v; want %q, %v", host, got, tc.host, tc.want)
			}
		})
	}
}

func TestNewRefusesAnEmptyHost(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("an empty host must not fall back to a fixed path")
	}
}
