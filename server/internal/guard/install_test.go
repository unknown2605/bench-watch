package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	cases := []struct{ name, in, bash, file string }{
		{"go", "bash=go\nfile=node\n", "go", "node"},
		{"shadow", "bash=shadow\nfile=shadow\n", "shadow", "shadow"},
		{"node", "bash=node\nfile=go\n", "node", "go"},
		{"unknown value", "bash=fast\nfile=go\n", "node", "go"},
		{"missing key", "bash=go\n", "go", "node"},
		{"empty file", "", "node", "node"},
		{"spaces and CR", " bash = go \r\nfile=\tshadow\r\n", "go", "shadow"},
		{"no final newline", "bash=go", "go", "node"},
		{"last line wins", "bash=go\nbash=shadow\n", "shadow", "node"},
		{"unknown later resets", "bash=go\nbash=oops\n", "node", "node"},
		{"line without equals", "bash\nfile=go\n", "node", "go"},
		{"ninth line", strings.Repeat("#\n", 8) + "bash=go\n", "node", "node"},
		{"eighth line", strings.Repeat("#\n", 7) + "bash=go\n", "go", "node"},
		{"long line", "bash=go\n" + strings.Repeat("x", 64) + "\n", "node", "node"},
	}
	for _, c := range cases {
		if b, f := parseMode([]byte(c.in), "bash"), parseMode([]byte(c.in), "file"); b != c.bash || f != c.file {
			t.Errorf("%s: bash %s file %s, want %s %s", c.name, b, f, c.bash, c.file)
		}
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sum12(body string) string {
	h := sha256.Sum256([]byte(body))
	return hex.EncodeToString(h[:])[:12]
}

func reader(t *testing.T) (*InstallReader, string, string) {
	t.Helper()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	root := t.TempDir()
	sys, user := filepath.Join(root, "sys"), filepath.Join(root, "user")
	return &InstallReader{SystemDir: sys, UserDir: user}, sys, user
}

func TestInstallSystemWithBuild(t *testing.T) {
	r, sys, user := reader(t)
	write(t, filepath.Join(sys, "guard-mode"), "bash=go\nfile=node\n")
	write(t, filepath.Join(sys, "MANIFEST.sha256"), "abc  guard.sh\n")
	write(t, filepath.Join(user, "guard-mode"), "bash=shadow\n")
	at := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(sys, "MANIFEST.sha256"), at, at); err != nil {
		t.Fatal(err)
	}
	in := r.Get()
	if in.Source != "system" || in.Bash != "go" || in.File != "node" || in.Build != sum12("abc  guard.sh\n") ||
		!in.InstalledAt.Equal(at) {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallUserUsesBinary(t *testing.T) {
	r, _, user := reader(t)
	write(t, filepath.Join(user, "guard-mode"), "bash=shadow\n")
	write(t, filepath.Join(user, "bin", "claude-guard"), "binary bytes")
	in := r.Get()
	if in.Source != "user" || in.Bash != "shadow" || in.File != "node" || in.Build != sum12("binary bytes") ||
		in.InstalledAt.IsZero() {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallNone(t *testing.T) {
	r, _, _ := reader(t)
	if in := r.Get(); in.Source != "none" || in.Bash != "unknown" || in.File != "unknown" || in.Build != "" ||
		!in.InstalledAt.IsZero() {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallVersionAbsentWithoutStamp(t *testing.T) {
	r, sys, _ := reader(t)
	write(t, filepath.Join(sys, "guard-mode"), "bash=go\n")
	if in := r.Get(); in.Source != "system" || in.Bash != "go" || in.Build != "" || !in.InstalledAt.IsZero() {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallRefusesSymlinkedAndOversizedModeFile(t *testing.T) {
	r, sys, _ := reader(t)
	write(t, filepath.Join(sys, "real-mode"), "bash=go\nfile=go\n")
	if err := os.Symlink(filepath.Join(sys, "real-mode"), filepath.Join(sys, "guard-mode")); err != nil {
		t.Fatal(err)
	}
	if in := r.Get(); in.Source != "system" || in.Bash != "unknown" || in.File != "unknown" {
		t.Fatalf("symlink: %+v", in)
	}
	if err := os.Remove(filepath.Join(sys, "guard-mode")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sys, "guard-mode"), "bash=go\n"+strings.Repeat("#", int(maxModeBytes)))
	if in := (&InstallReader{SystemDir: sys, UserDir: sys}).Get(); in.Bash != "unknown" || in.File != "unknown" {
		t.Fatalf("oversized: %+v", in)
	}
}

// A parser-valid file cannot reach 4 KiB (8 lines under 64 characters), so a 4096-byte file reads as the
// documented "node" for an over-long line; "unknown" would mean the reader refused it.
func TestInstallReadsExactly4KiBButNotOneByteMore(t *testing.T) {
	r, sys, _ := reader(t)
	mode := filepath.Join(sys, "guard-mode")
	write(t, mode, "bash=go\n"+strings.Repeat("#", int(maxModeBytes)-8))
	if in := r.Get(); in.Bash != "node" || in.File != "node" {
		t.Fatalf("4096 bytes: %+v, want node (read, long line)", in)
	}
	write(t, mode, "bash=go\n"+strings.Repeat("#", int(maxModeBytes)-7))
	if in := (&InstallReader{SystemDir: sys}).Get(); in.Bash != "unknown" {
		t.Fatalf("4097 bytes: %+v, want unknown", in)
	}
}

func lowerCaps(t *testing.T, mode, manifest, binary int64) {
	t.Helper()
	m, f, b := maxModeBytes, maxManifest, maxBinaryBytes
	maxModeBytes, maxManifest, maxBinaryBytes = mode, manifest, binary
	t.Cleanup(func() { maxModeBytes, maxManifest, maxBinaryBytes = m, f, b })
}

// within fails the test instead of hanging when f blocks.
func within(t *testing.T, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("read blocked")
	}
}

func TestInstallDirectoryInPlaceOfModeFile(t *testing.T) {
	r, sys, _ := reader(t)
	if err := os.MkdirAll(filepath.Join(sys, "guard-mode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if in := r.Get(); in.Source != "system" || in.Bash != "unknown" || in.File != "unknown" {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallFifoInPlaceOfModeFileAndStamp(t *testing.T) {
	r, sys, _ := reader(t)
	if err := os.MkdirAll(sys, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"guard-mode", "MANIFEST.sha256"} {
		if err := syscall.Mkfifo(filepath.Join(sys, name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var in Install
	within(t, func() { in = r.Get() })
	if in.Source != "system" || in.Bash != "unknown" || in.File != "unknown" || in.Build != "" ||
		!in.InstalledAt.IsZero() {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallStampOverItsCap(t *testing.T) {
	lowerCaps(t, 4<<10, 16, 16)
	r, sys, _ := reader(t)
	write(t, filepath.Join(sys, "guard-mode"), "bash=go\n")
	write(t, filepath.Join(sys, "MANIFEST.sha256"), strings.Repeat("x", 17))
	if in := r.Get(); in.Bash != "go" || in.Build != "" || !in.InstalledAt.IsZero() {
		t.Fatalf("system stamp over cap: %+v", in)
	}
	write(t, filepath.Join(sys, "MANIFEST.sha256"), strings.Repeat("x", 16))
	if in := (&InstallReader{SystemDir: sys}).Get(); in.Build == "" {
		t.Fatalf("stamp at cap must be read: %+v", in)
	}
	u := &InstallReader{UserDir: filepath.Join(sys, "u")}
	write(t, filepath.Join(sys, "u", "guard-mode"), "bash=go\n")
	write(t, filepath.Join(sys, "u", "bin", "claude-guard"), strings.Repeat("x", 17))
	if in := u.Get(); in.Source != "user" || in.Build != "" {
		t.Fatalf("binary over cap: %+v", in)
	}
}

func TestInstallSymlinkedFolderIsUnknownAndNeverFallsThrough(t *testing.T) {
	r, sys, user := reader(t)
	real := filepath.Join(filepath.Dir(sys), "real")
	write(t, filepath.Join(real, "guard-mode"), "bash=go\nfile=go\n")
	write(t, filepath.Join(real, "MANIFEST.sha256"), "abc")
	write(t, filepath.Join(user, "guard-mode"), "bash=shadow\n")
	if err := os.Symlink(real, sys); err != nil {
		t.Fatal(err)
	}
	in := r.Get()
	if in.Source != "system" || in.Bash != "unknown" || in.File != "unknown" || in.Build != "" ||
		!in.InstalledAt.IsZero() {
		t.Fatalf("symlinked system folder: %+v", in)
	}
	if err := os.Remove(sys); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, user+"-link"); err != nil {
		t.Fatal(err)
	}
	u := &InstallReader{SystemDir: sys, UserDir: user + "-link"}
	if in := u.Get(); in.Source != "user" || in.Bash != "unknown" || in.Build != "" {
		t.Fatalf("symlinked user folder: %+v", in)
	}
}

func TestInstallLogsOnlyWhenFailureStateChanges(t *testing.T) {
	r, sys, _ := reader(t)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetFlags(log.LstdFlags) })
	refresh := func() {
		now = now.Add(2 * installTTL)
		r.Get()
	}
	write(t, filepath.Join(sys, "guard-mode"), "bash=go\n")
	for range 3 {
		refresh()
	}
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("missing stamp over 3 refreshes: %d lines, want 1", n)
	}
	write(t, filepath.Join(sys, "MANIFEST.sha256"), "abc")
	refresh()
	if n := strings.Count(buf.String(), "\n"); n != 1 {
		t.Fatalf("recovery logged: %d lines", n)
	}
	if err := os.Remove(filepath.Join(sys, "MANIFEST.sha256")); err != nil {
		t.Fatal(err)
	}
	refresh()
	if n := strings.Count(buf.String(), "\n"); n != 2 {
		t.Fatalf("failure after recovery: %d lines, want 2", n)
	}
}

func TestInstallConcurrentGet(t *testing.T) {
	r, sys, _ := reader(t)
	write(t, filepath.Join(sys, "guard-mode"), "bash=go\n")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if in := r.Get(); in.Source != "system" || in.Bash != "go" {
					t.Errorf("install %+v", in)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestParseModeCountsBytesNotCharacters(t *testing.T) {
	multi := "bash=go\n#" + strings.Repeat("é", 40) + "\n"
	if b := parseMode([]byte(multi), "bash"); b != "node" {
		t.Errorf("41 characters, 81 bytes: %s, want node", b)
	}
	ascii := func(n int) string { return "bash=go\n#" + strings.Repeat("a", n-1) + "\n" }
	if b := parseMode([]byte(ascii(63)), "bash"); b != "go" {
		t.Errorf("63 bytes: %s, want go", b)
	}
	if b := parseMode([]byte(ascii(64)), "bash"); b != "node" {
		t.Errorf("64 bytes: %s, want node", b)
	}
}

func TestParseModeStripsOnlyASCIISpace(t *testing.T) {
	if b := parseMode([]byte("bash\v=\fgo\x20\n"), "bash"); b != "go" {
		t.Errorf("vertical tab and form feed: %s, want go", b)
	}
	if b := parseMode([]byte("bash=go\u00a0\n"), "bash"); b != "node" {
		t.Errorf("no-break space is not stripped: %s, want node", b)
	}
}

func TestInstallRefusesSymlinkedStampAndMissingMode(t *testing.T) {
	r, sys, _ := reader(t)
	write(t, filepath.Join(sys, "target"), "x")
	if err := os.Symlink(filepath.Join(sys, "target"), filepath.Join(sys, "MANIFEST.sha256")); err != nil {
		t.Fatal(err)
	}
	if in := r.Get(); in.Source != "system" || in.Bash != "unknown" || in.Build != "" || !in.InstalledAt.IsZero() {
		t.Fatalf("install %+v", in)
	}
}

func TestInstallCachesForAMinute(t *testing.T) {
	r, sys, _ := reader(t)
	now := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	mode := filepath.Join(sys, "guard-mode")
	write(t, mode, "bash=go\n")
	if r.Get().Bash != "go" {
		t.Fatal("first read")
	}
	write(t, mode, "bash=shadow\n")
	now = now.Add(59 * time.Second)
	if got := r.Get().Bash; got != "go" {
		t.Fatalf("within a minute: %s, want the cached go", got)
	}
	now = now.Add(time.Second)
	if got := r.Get().Bash; got != "shadow" {
		t.Fatalf("after a minute: %s, want shadow", got)
	}
	now = now.Add(-time.Hour)
	write(t, mode, "bash=node\n")
	if got := r.Get().Bash; got != "node" {
		t.Fatalf("clock moved back: %s, want a fresh read", got)
	}
}

func TestInstallLogLineHasNoPath(t *testing.T) {
	r, sys, _ := reader(t)
	write(t, filepath.Join(sys, "secret-folder-name", "x"), "x")
	write(t, filepath.Join(sys, "guard-mode"), strings.Repeat("#", int(maxModeBytes)+1))
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetFlags(log.LstdFlags) })
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r.Get()
	out := buf.String()
	if strings.Count(out, "\n") != 1 || strings.Contains(out, sys) || strings.Contains(out, "/") ||
		strings.Contains(out, "guard-mode") {
		t.Fatalf("log output %q, want one line without a path", out)
	}
}
