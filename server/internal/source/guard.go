package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var (
	ErrNotRegular    = errors.New("ENOTREG")
	ErrOutsideRoots  = errors.New("path outside the reader's roots")
	ErrForbiddenName = errors.New("file name looks like credentials")
	ErrRelativePath  = errors.New("path is not absolute and clean")
)

// Agents keep auth next to their sessions (Gemini oauth_creds.json, Codex auth.json, Copilot tokens); none is usage.
var forbiddenName = regexp.MustCompile(`(?i)(auth|cred|token|secret|passw|cookie|account|apikey|api[._-]key|` +
	`(^|[._-])keys?([._-]|$)|^\.env|\.pem$|\.p12$|\.pfx$|\.key$|^id_(rsa|dsa|ecdsa|ed25519)|keychain|\.netrc$)`)

// Check refuses a path that is not absolute and clean, lies outside every root, or whose file name looks like
// credentials. Folder names are the user's project names, so only the base name is matched; O_NOFOLLOW is Open's.
func Check(roots []string, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%w: %q", ErrRelativePath, path)
	}
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return fmt.Errorf("%w: root %q", ErrRelativePath, root)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if name := filepath.Base(path); forbiddenName.MatchString(name) {
			return fmt.Errorf("%w: %q", ErrForbiddenName, name)
		}
		return nil
	}
	return fmt.Errorf("%w: %q", ErrOutsideRoots, path)
}

// Open is the only way a reader opens a file: Check, then OpenRegular.
func Open(roots []string, path string) (*os.File, int64, error) {
	if err := Check(roots, path); err != nil {
		return nil, 0, err
	}
	return OpenRegular(path)
}

// OpenRegular opens without following a link; O_NONBLOCK keeps open() from hanging on a FIFO, which fstat rejects.
func OpenRegular(file string) (*os.File, int64, error) {
	fd, err := syscall.Open(file, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, &os.PathError{Op: "open", Path: "file", Err: err}
	}
	f := os.NewFile(uintptr(fd), "file")
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !st.Mode().IsRegular() {
		f.Close()
		return nil, 0, ErrNotRegular
	}
	return f, st.Size(), nil
}
