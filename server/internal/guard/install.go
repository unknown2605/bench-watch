package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// SystemRoot is the root-owned guard install.
	SystemRoot = "/usr/local/libexec/claude-guard"

	installTTL   = time.Minute
	modeFile     = "guard-mode"
	manifestFile = "MANIFEST.sha256"
	binaryFile   = "bin/claude-guard"
)

// Size caps in bytes; variables so a test can lower them.
var (
	maxModeBytes   int64 = 4 << 10
	maxBinaryBytes int64 = 64 << 20
	maxManifest    int64 = 1 << 20
)

// Install is the configured guard in effect. Source is system, user or none; the modes are go, node, shadow or
// unknown; InstalledAt is zero and Build empty when they cannot be read.
type Install struct {
	Source      string
	Bash, File  string
	InstalledAt time.Time
	Build       string
}

// Found is true when a guard is installed: a folder alone, such as a hooks folder with other hooks, is not one.
func (i Install) Found() bool {
	return i.Source != "none" && (i.Build != "" || i.Bash != "unknown" || i.File != "unknown")
}

// InstallReader reads the guard install read-only and at most once a minute.
type InstallReader struct {
	SystemDir string
	UserDir   string
	Now       func() time.Time

	mu   sync.Mutex
	at   time.Time
	last Install
	have bool
	bad  string
}

// NewInstallReader returns a reader with the default roots: the system folder and <home>/.claude/hooks.
func NewInstallReader(home string) *InstallReader {
	return &InstallReader{SystemDir: SystemRoot, UserDir: filepath.Join(home, ".claude", "hooks")}
}

// Get returns the cached install, re-reading it when the cache is a minute old.
func (r *InstallReader) Get() Install {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if r.have && now.Sub(r.at) < installTTL && !now.Before(r.at) {
		return r.last
	}
	r.last, r.at, r.have = r.read(), now, true
	return r.last
}

func (r *InstallReader) read() Install {
	var dir, source string
	unusable := false
	for _, c := range []struct{ path, name string }{{r.SystemDir, "system"}, {r.UserDir, "user"}} {
		st, err := os.Lstat(c.path)
		if err != nil && (errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)) {
			continue
		}
		dir, source, unusable = c.path, c.name, err != nil || !st.IsDir()
		break
	}
	if source == "" {
		r.note("")
		return Install{Source: "none", Bash: "unknown", File: "unknown"}
	}
	in := Install{Source: source, Bash: "unknown", File: "unknown"}
	if unusable {
		r.note(source)
		return in
	}
	failed := false
	if raw, ok := readRegular(filepath.Join(dir, modeFile), maxModeBytes); ok {
		in.Bash, in.File = parseMode(raw, "bash"), parseMode(raw, "file")
	} else {
		failed = true
	}
	stamp, limit := binaryFile, maxBinaryBytes
	if source == "system" {
		stamp, limit = manifestFile, maxManifest
	}
	if mt, sum, ok := fingerprint(filepath.Join(dir, stamp), limit); ok {
		in.InstalledAt, in.Build = mt, sum
	} else {
		failed = true
	}
	if failed {
		r.note(source)
	} else {
		r.note("")
	}
	return in
}

// note logs a read failure once, when the failing source differs from the previous refresh; "" means all read.
func (r *InstallReader) note(failing string) {
	if failing != "" && failing != r.bad {
		log.Printf("guard install: some files could not be read (source %s)", failing)
	}
	r.bad = failing
}

// openRegular opens p without following a symlink or blocking, and returns it only when it is the regular file
// Lstat saw, within limit bytes.
func openRegular(p string, limit int64) (*os.File, os.FileInfo, bool) {
	lst, err := os.Lstat(p)
	if err != nil || !lst.Mode().IsRegular() {
		return nil, nil, false
	}
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, false
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || !os.SameFile(lst, st) || st.Size() > limit {
		f.Close()
		return nil, nil, false
	}
	return f, st, true
}

func readRegular(p string, limit int64) ([]byte, bool) {
	f, _, ok := openRegular(p, limit)
	if !ok {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, false
	}
	return b, true
}

// fingerprint returns the modification time and the first 12 hex of the SHA-256 of a regular file within limit.
func fingerprint(p string, limit int64) (time.Time, string, bool) {
	f, st, ok := openRegular(p, limit)
	if !ok {
		return time.Time{}, "", false
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return time.Time{}, "", false
	}
	return st.ModTime().UTC(), hex.EncodeToString(h.Sum(nil))[:12], true
}

// stripSpace removes the characters [[:space:]] matches in the C locale.
func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || (r >= '\t' && r <= '\r') {
			return -1
		}
		return r
	}, s)
}

// parseMode mirrors guard.sh: lines are KEY=VALUE with whitespace removed, the last line for the key wins, an
// unknown value counts as node, and more than 8 lines or a line of 64 bytes or more counts as node.
func parseMode(raw []byte, key string) string {
	mode := "node"
	text := string(raw)
	lines := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		if i+1 > 8 || len(line) >= 64 {
			return "node"
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if stripSpace(k) != key {
			continue
		}
		switch v = stripSpace(v); v {
		case "go", "shadow", "node":
			mode = v
		default:
			mode = "node"
		}
	}
	return mode
}
