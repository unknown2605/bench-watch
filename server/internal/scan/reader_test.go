package scan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/source"
)

// strayReader lists a file its roots do not hold, as a buggy or tricked reader would.
type strayReader struct {
	*ClaudeReader
	path string
}

func (r strayReader) Discover() (source.Discovery, error) {
	return source.Discovery{Units: []source.Unit{{Client: clients.ClaudeCode,
		Key: source.Key(clients.ClaudeCode, "x"), Path: r.path, Rel: "x", Size: 2}}}, nil
}

func TestTheStoreOpensNothingOutsideAReadersRootsOrNamedLikeCredentials(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "projects")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		want error
	}{
		{filepath.Join(home, "settings.jsonl"), source.ErrOutsideRoots},
		{filepath.Join(root, "-p", "auth.jsonl"), source.ErrForbiddenName},
	} {
		if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cache, err := NewCacheFor([]source.Reader{strayReader{NewClaudeReader(root), c.path}}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		st, err := cache.Update(nil)
		if err != nil {
			t.Fatal(err)
		}
		if st.Unreadable != 1 || !errors.Is(st.FileErr, c.want) || len(st.Entries) != 0 {
			t.Errorf("%s: unreadable %d, err %v, entries %d; want 1, %v, 0", c.path, st.Unreadable, st.FileErr,
				len(st.Entries), c.want)
		}
	}
}

func TestACacheRefusesTwoReadersForOneClient(t *testing.T) {
	r := NewClaudeReader(t.TempDir())
	if _, err := NewCacheFor([]source.Reader{r, r}, t.TempDir()); err == nil {
		t.Fatal("two Claude Code readers were accepted")
	}
}
