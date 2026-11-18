package docker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Presence says whether a Docker socket was found for this machine.
type Presence int

const (
	// Found: Host is a unix socket URL to use, whether or not the daemon answers.
	Found Presence = iota
	// Absent: no candidate socket exists; the machine has no Docker.
	Absent
	// Unsupported: DOCKER_HOST names another scheme; it is treated as absent and never dialled.
	Unsupported
)

// Env is the machine the socket is looked up on; tests fill it in.
type Env struct {
	Getenv func(string) string
	GOOS   string
	Home   string
	// Exists reports whether a path is there, a dangling link included.
	Exists func(string) bool
	// DockerApp is the Docker Desktop bundle on macOS; empty skips the check.
	DockerApp string
}

// DesktopApp is where Docker Desktop installs itself on macOS.
const DesktopApp = "/Applications/Docker.app"

// OSEnv is the running machine.
func OSEnv() Env {
	home, _ := os.UserHomeDir()
	return Env{Getenv: os.Getenv, GOOS: runtime.GOOS, Home: home, DockerApp: DesktopApp, Exists: func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	}}
}

// Resolve picks the Docker socket: DOCKER_HOST when set (unix:// only), else the first platform default that
// exists. A dangling link, or on macOS the Docker Desktop app, counts as present, so Docker that is installed but
// stopped is not absent: Desktop removes its socket on quit.
func Resolve(e Env) (string, Presence) {
	if h := e.Getenv("DOCKER_HOST"); h != "" {
		path, ok := strings.CutPrefix(h, "unix://")
		if !ok || !filepath.IsAbs(path) {
			return "", Unsupported
		}
		return h, Found
	}
	var candidates []string
	switch e.GOOS {
	case "darwin":
		if e.Home != "" {
			candidates = append(candidates, filepath.Join(e.Home, ".docker", "run", "docker.sock"))
		}
		candidates = append(candidates, "/var/run/docker.sock")
	case "linux":
		candidates = append(candidates, "/var/run/docker.sock")
		if x := e.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(x) {
			candidates = append(candidates, filepath.Join(x, "docker.sock"))
		}
	default:
		candidates = append(candidates, "/var/run/docker.sock")
	}
	for _, c := range candidates {
		if e.Exists(c) {
			return "unix://" + c, Found
		}
	}
	if e.GOOS == "darwin" && e.DockerApp != "" && e.Exists(e.DockerApp) {
		return "unix://" + candidates[0], Found
	}
	return "", Absent
}
