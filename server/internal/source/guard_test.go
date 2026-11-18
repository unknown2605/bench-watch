package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckRefusesPathsOutsideTheRoots(t *testing.T) {
	roots := []string{"/home/u/.claude/projects", "/home/u/.codex/sessions"}
	for _, path := range []string{
		"/home/u/.claude/settings.json", "/home/u/.claude/projects-old/a.jsonl", "/etc/passwd",
		"/home/u/.claude/projects", "/home/u/.codex/sessions/../config.toml",
	} {
		err := Check(roots, path)
		if !errors.Is(err, ErrOutsideRoots) && !errors.Is(err, ErrRelativePath) {
			t.Errorf("%s: got %v, want a refusal", path, err)
		}
	}
	if err := Check(roots, "relative/a.jsonl"); !errors.Is(err, ErrRelativePath) {
		t.Errorf("relative path: got %v", err)
	}
	if err := Check([]string{"rel"}, "/rel/a.jsonl"); !errors.Is(err, ErrRelativePath) {
		t.Errorf("relative root: got %v", err)
	}
}

func TestCheckRefusesCredentialNames(t *testing.T) {
	root := "/home/u/.gemini"
	for _, name := range []string{
		"auth.json", "oauth_creds.json", "google_accounts.json", "token", "copilot-token.json", "api.key",
		"apikey.txt", "keys.json", "client_secret.json", ".env", ".env.local", "cert.pem", "id_ed25519",
		"cookies.sqlite", "AUTH.JSON", "Credentials", "x.p12", "passwords.txt",
	} {
		if err := Check([]string{root}, filepath.Join(root, name)); !errors.Is(err, ErrForbiddenName) {
			t.Errorf("%s: got %v, want ErrForbiddenName", name, err)
		}
	}
}

func TestCheckAllowsUsageRecords(t *testing.T) {
	root := "/home/u/.claude/projects"
	for _, rel := range []string{
		"-home-u-work-app/11111111-aaaa-4aaa-8aaa-111111111111.jsonl",
		"-home-u-work-app/11111111-aaaa-4aaa-8aaa-111111111111/subagents/agent-a1.meta.json",
		"-home-u-work-monkey/rollout-2026-10-10.jsonl",
		"-home-u-work-auth-service/22222222-bbbb-4bbb-8bbb-222222222222.jsonl",
	} {
		if err := Check([]string{root}, filepath.Join(root, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestOpenRefusesBeforeTouchingTheFile(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "auth.json")
	if err := os.WriteFile(secret, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, _, err := Open([]string{root}, secret); !errors.Is(err, ErrForbiddenName) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("got %v, want ErrForbiddenName", err)
	}
	outside := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open([]string{root}, outside); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("got %v, want ErrOutsideRoots", err)
	}
	link := filepath.Join(root, "a.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open([]string{root}, link); err == nil {
		t.Fatal("followed a link out of the root")
	}
}
