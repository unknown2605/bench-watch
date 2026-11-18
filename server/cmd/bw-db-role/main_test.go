package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// RFC 7677 section 3: user "user", password "pencil".
const (
	rfcSalt        = "W22ZaJ0SNY7soEsUEjb6gQ=="
	rfcAuthMessage = "n=user,r=rOprNGfwEbeRWgbNEkqO," +
		"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096," +
		"c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0"
	rfcClientProof     = "dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="
	rfcServerSignature = "6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="
)

func TestScramVerifierMatchesRFC7677(t *testing.T) {
	salt, _ := base64.StdEncoding.DecodeString(rfcSalt)
	v, err := scramVerifier("pencil", salt, 4096)
	if err != nil {
		t.Fatal(err)
	}
	head, keys, ok := strings.Cut(v, "$")
	if head != "SCRAM-SHA-256" || !ok {
		t.Fatalf("bad prefix in %q", v)
	}
	params, keyPair, _ := strings.Cut(keys, "$")
	if params != "4096:"+rfcSalt {
		t.Fatalf("iterations and salt: %q", params)
	}
	stored64, server64, _ := strings.Cut(keyPair, ":")
	storedKey, _ := base64.StdEncoding.DecodeString(stored64)
	serverKey, _ := base64.StdEncoding.DecodeString(server64)

	if got := base64.StdEncoding.EncodeToString(hmacSHA256(serverKey, rfcAuthMessage)); got != rfcServerSignature {
		t.Fatalf("server signature %s, want %s", got, rfcServerSignature)
	}
	proof, _ := base64.StdEncoding.DecodeString(rfcClientProof)
	sig := hmac.New(sha256.New, storedKey)
	sig.Write([]byte(rfcAuthMessage))
	clientKey := sig.Sum(nil)
	for i := range clientKey {
		clientKey[i] ^= proof[i]
	}
	if sum := sha256.Sum256(clientKey); !hmac.Equal(sum[:], storedKey) {
		t.Fatal("client proof does not verify against the stored key")
	}
}

func TestPassfileLineEscapes(t *testing.T) {
	o := options{host: "127.0.0.1", port: 5432, database: "benchwatch", role: "benchwatch"}
	if got := passfileLine(o, `a:b\c`); got != `127.0.0.1:5432:benchwatch:benchwatch:a\:b\\c`+"\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPasswordsAreLongAndPassfileSafe(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		p, err := newPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 43 || strings.ContainsAny(p, `:\'"$ `) || seen[p] {
			t.Fatalf("unexpected password shape (length %d)", len(p))
		}
		seen[p] = true
	}
}

func TestValidateAcceptsOnlyNamesPgHbaConfines(t *testing.T) {
	good := options{passfile: "/tmp/x", database: "benchwatch", role: "benchwatch", host: "127.0.0.1", port: 5432}
	if err := validate(good); err != nil {
		t.Fatalf("good options refused: %v", err)
	}
	for _, name := range []string{"benchwatch_cm00002_test", "benchwatch__", "benchwatch_1",
		"benchwatch_" + strings.Repeat("a", 52)} {
		o := good
		o.role, o.database = name, name
		if err := validate(o); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	both := func(name string) func(o *options) { return func(o *options) { o.role, o.database = name, name } }
	cases := map[string]func(o *options){
		"no passfile":         func(o *options) { o.passfile = "" },
		"postgres role":       both("postgres"),
		"pg_ role":            both("pg_monitor"),
		"unconfined name":     both("monitor"),
		"trailing underscore": both("benchwatch_"),
		"suffix without _":    both("benchwatchx"),
		"prefix":              both("xbenchwatch"),
		"upper case":          both("Benchwatch"),
		"upper suffix":        both("benchwatch_X"),
		"dash suffix":         both("benchwatch_a-b"),
		"trailing newline":    both("benchwatch\n"),
		"newline inside":      both("benchwatch\n_x"),
		"over 63 characters":  both("benchwatch_" + strings.Repeat("a", 53)),
		"quoted role":         both(`benchwatch_"; DROP ROLE x; --`),
		"database differs":    func(o *options) { o.database = "benchwatch_other" },
		"role differs":        func(o *options) { o.role = "benchwatch_other" },
		"system database":     func(o *options) { o.database = "postgres" },
		"template database":   func(o *options) { o.database = "template1" },
		"colon host":          func(o *options) { o.host = "a:b" },
		"bad port":            func(o *options) { o.port = 0 },
	}
	for name, mutate := range cases {
		o := good
		mutate(&o)
		if validate(o) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckTargetRefusesLinksAndOpenFolders(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "pgpass")
	if err := checkTarget(target); err != nil {
		t.Fatalf("absent target refused: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), target); err != nil {
		t.Fatal(err)
	}
	if checkTarget(target) == nil {
		t.Fatal("symlink accepted")
	}
	open := filepath.Join(dir, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil {
		t.Fatal(err)
	}
	if checkTarget(filepath.Join(open, "pgpass")) == nil {
		t.Fatal("world-writable folder accepted")
	}
}

func TestWriteTempIsPrivate(t *testing.T) {
	dir := t.TempDir()
	name, err := writeTemp(filepath.Join(dir, "pgpass"), "line\n")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}

func TestRefusalChecksEverythingFirst(t *testing.T) {
	o := options{role: "benchwatch", database: "benchwatch"}
	own := existing{roleFound: true, marked: true, dbFound: true, dbOwner: "benchwatch"}
	cases := map[string]struct {
		e      existing
		refuse bool
	}{
		"fresh install":                {existing{}, false},
		"rotation of its own role":     {own, false},
		"own role, database missing":   {existing{roleFound: true, marked: true}, false},
		"database without the role":    {existing{dbFound: true, dbOwner: "someone"}, true},
		"unmarked role of the name":    {existing{roleFound: true}, true},
		"marked superuser":             {existing{roleFound: true, marked: true, super: true}, true},
		"marked role with memberships": {existing{roleFound: true, marked: true, memberships: 1}, true},
		"marked role owning elsewhere": {existing{roleFound: true, marked: true, foreignObjs: 2}, true},
		"database of another owner":    {existing{roleFound: true, marked: true, dbFound: true, dbOwner: "x"}, true},
	}
	for name, c := range cases {
		if err := refusal(o, c.e); (err != nil) != c.refuse {
			t.Errorf("%s: refusal = %v, want refuse %v", name, err, c.refuse)
		}
	}
}

func TestSQLErrorKeepsOnlySafeCauses(t *testing.T) {
	err := sqlError("admin connection", fmt.Errorf("dial host=secret-host: %w", context.DeadlineExceeded))
	if got := err.Error(); got != "admin connection: timed out" {
		t.Fatalf("got %q", got)
	}
	err = sqlError("step", errors.New("password=hunter2 rejected"))
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("cause text leaked: %q", err)
	}
}

func TestCommitThenReplaceKeepsNoOldPassword(t *testing.T) {
	setup := func(t *testing.T) (dir, tmp, target string) {
		dir = t.TempDir()
		target = filepath.Join(dir, "pgpass")
		if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tmp, err := writeTemp(target, "new\n")
		if err != nil {
			t.Fatal(err)
		}
		return dir, tmp, target
	}
	files := func(t *testing.T, dir string) []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	content := func(t *testing.T, path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("commit lands", func(t *testing.T) {
		dir, tmp, target := setup(t)
		committed := false
		commit := func(context.Context) error { committed = true; return nil }
		if err := commitThenReplace(context.Background(), commit, tmp, target); err != nil {
			t.Fatal(err)
		}
		if !committed || content(t, target) != "new\n" {
			t.Fatalf("committed %v, passfile %q", committed, content(t, target))
		}
		if got := files(t, dir); len(got) != 1 || got[0] != "pgpass" {
			t.Fatalf("files left: %v", got)
		}
	})
	t.Run("commit fails", func(t *testing.T) {
		dir, tmp, target := setup(t)
		err := commitThenReplace(context.Background(), func(context.Context) error { return errors.New("x") }, tmp, target)
		if err == nil || !strings.Contains(err.Error(), "run bw-db-role again") {
			t.Fatalf("error %v", err)
		}
		os.Remove(tmp)
		if content(t, target) != "old\n" {
			t.Fatal("a failed commit replaced the passfile")
		}
		if got := files(t, dir); len(got) != 1 {
			t.Fatalf("files left besides the passfile: %v", got)
		}
	})
	t.Run("interrupted before the commit", func(t *testing.T) {
		_, tmp, target := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		if err := commitThenReplace(ctx, func(context.Context) error { called = true; return nil }, tmp, target); err == nil {
			t.Fatal("no error after an interrupt")
		}
		if called || content(t, target) != "old\n" {
			t.Fatalf("commit called %v, passfile %q", called, content(t, target))
		}
	})
	t.Run("interrupted during the commit", func(t *testing.T) {
		_, tmp, target := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		commit := func(c context.Context) error {
			cancel()
			return c.Err()
		}
		if err := commitThenReplace(ctx, commit, tmp, target); err != nil {
			t.Fatalf("a signal during the commit stopped it: %v", err)
		}
		if content(t, target) != "new\n" {
			t.Fatal("the commit landed but the passfile was not replaced")
		}
	})
	t.Run("rename fails after the commit", func(t *testing.T) {
		dir, tmp, _ := setup(t)
		err := commitThenReplace(context.Background(), func(context.Context) error { return nil }, tmp,
			filepath.Join(dir, "missing", "pgpass"))
		if err == nil || !strings.Contains(err.Error(), "password was changed") {
			t.Fatalf("error %v", err)
		}
	})
}

func TestRemoveStaleTempsOnlyOldOwnTemps(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "pgpass")
	old := time.Now().Add(-time.Hour)
	write := func(name string, mod time.Time) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(roleLine), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	write("pgpass", old)
	write(tempPrefix+"123", old)
	write(tempPrefix+"456", old)
	write(tempPrefix+"789", time.Now())
	write("other", old)
	if err := os.Symlink(filepath.Join(dir, "other"), filepath.Join(dir, tempPrefix+"1")); err != nil {
		t.Fatal(err)
	}
	n, err := removeStaleTemps(target, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v; want 2", n, err)
	}
	for _, name := range []string{"pgpass", tempPrefix + "789", "other", tempPrefix + "1"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed", name)
		}
	}
}

func writeAged(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// A dated backup has the shape earlier versions gave their temps, and the content of one (CM-00009 Q2-L2).
func TestRemoveStaleTempsKeepsADatedBackup(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	kept := []string{".pgpass-20261109", ".pgpass-old-20261109", ".pgpass-123", ".pgpass-old-456"}
	for _, name := range append(kept, tempPrefix+"5") {
		writeAged(t, filepath.Join(dir, name), roleLine, old)
	}
	n, err := removeStaleTemps(filepath.Join(dir, "pgpass"), time.Now())
	if err != nil || n != 1 {
		t.Errorf("removed %d, %v; want only the helper's own temp", n, err)
	}
	for _, name := range kept {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s removed", name)
		}
	}
}

func TestRemoveCheckedKeepsAFileThatTookTheName(t *testing.T) {
	dir := t.TempDir()
	path, other := filepath.Join(dir, tempPrefix+"1"), filepath.Join(dir, "other")
	writeAged(t, path, roleLine, time.Now())
	checked, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeAged(t, other, "dummy\n", time.Now())
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if err := removeChecked(path, checked); !errors.Is(err, errReplaced) {
		t.Errorf("removeChecked: %v, want errReplaced", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "dummy\n" {
		t.Error("the file that took the name was removed")
	}
	if checked, err = os.Lstat(path); err != nil {
		t.Fatal(err)
	}
	if err := removeChecked(path, checked); err != nil {
		t.Errorf("removeChecked on the checked file: %v", err)
	}
}

func TestRemoveStaleTempsKeepsOtherPgpassNames(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	for _, target := range []string{".pgpass-x", tempPrefix + "999", ".pgpass-old-999"} {
		dir := t.TempDir()
		kept := []string{target, ".pgpass-work", ".pgpass-123", ".pgpass-old-456", tempPrefix + "12a", tempPrefix,
			tempPrefix + "-1", "x" + tempPrefix + "1"}
		for _, name := range append(kept, tempPrefix+"123", tempPrefix+"456") {
			writeAged(t, filepath.Join(dir, name), roleLine, old)
		}
		n, err := removeStaleTemps(filepath.Join(dir, target), time.Now())
		if err != nil || n != 2 {
			t.Errorf("target %s: removed %d, %v; want the two temp files", target, n, err)
		}
		for _, name := range kept {
			if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
				t.Errorf("target %s: %s removed", target, name)
			}
		}
	}
}

const roleLine = "127.0.0.1:5432:benchwatch_x:benchwatch_x:x\\:y\n"

func TestRemoveStaleTempsKeepsFilesNotShapedLikeItsOwn(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	contents := map[string]string{
		tempPrefix + "1": "127.0.0.1:5432:app:app:x\n",
		tempPrefix + "2": roleLine + roleLine,
		tempPrefix + "3": strings.TrimSuffix(roleLine, "\n"),
		tempPrefix + "4": "127.0.0.1:5432:benchwatch:postgres:x\n",
		tempPrefix + "5": "*:*:*:*:x\n",
		tempPrefix + "6": "127.0.0.1:5432:benchwatch:benchwatch:\n",
		tempPrefix + "7": strings.Repeat("x", 2000) + "\n",
	}
	for name, content := range contents {
		writeAged(t, filepath.Join(dir, name), content, old)
	}
	writeAged(t, filepath.Join(dir, tempPrefix+"8"), roleLine, old)
	if err := os.Chmod(filepath.Join(dir, tempPrefix+"8"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAged(t, filepath.Join(dir, tempPrefix+"9"), roleLine, old)
	if err := os.Link(filepath.Join(dir, tempPrefix+"9"), filepath.Join(dir, "kept-link")); err != nil {
		t.Fatal(err)
	}
	writeAged(t, filepath.Join(dir, tempPrefix+"10"), roleLine, old)
	n, err := removeStaleTemps(filepath.Join(dir, "pgpass"), time.Now())
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v; want only the one temp the helper could have written", n, err)
	}
	for _, name := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		if _, err := os.Lstat(filepath.Join(dir, tempPrefix+name)); err != nil {
			t.Errorf("%s removed", tempPrefix+name)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "kept-link")); err != nil {
		t.Error("kept-link removed")
	}
}

func TestRemoveStaleTempsSkipsOneItCannotRemove(t *testing.T) {
	chflags, err := exec.LookPath("chflags")
	if err != nil {
		t.Skip("needs chflags")
	}
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	locked, free := filepath.Join(dir, tempPrefix+"111"), filepath.Join(dir, tempPrefix+"222")
	writeAged(t, locked, roleLine, old)
	writeAged(t, free, roleLine, old)
	if out, err := exec.Command(chflags, "uchg", locked).CombinedOutput(); err != nil {
		t.Skipf("chflags: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command(chflags, "nouchg", locked).Run() })
	n, err := removeStaleTemps(filepath.Join(dir, "pgpass"), time.Now())
	if n != 1 || err == nil || !strings.Contains(err.Error(), "could not remove 1") {
		t.Errorf("removed %d, %v; want one removed and one counted as skipped", n, err)
	}
	if _, err := os.Lstat(free); err == nil {
		t.Error("the temp after the locked one was kept")
	}
}

type fakeRow struct {
	changed *bool
	err     error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.changed == nil {
		return errors.New("cannot scan NULL into *bool")
	}
	*dest[0].(*bool) = *r.changed
	return nil
}

type fakeQuerier struct{ row fakeRow }

func (q fakeQuerier) QueryRow(context.Context, string, ...any) pgx.Row { return q.row }

func TestCheckHbaLoadedRefusesAFileChangedSinceTheLoad(t *testing.T) {
	yes, no := true, false
	cases := map[string]struct {
		row fakeRow
		ok  bool
	}{
		"loaded after the last change": {fakeRow{changed: &no}, true},
		"changed after the load":       {fakeRow{changed: &yes}, false},
		"modification time is NULL":    {fakeRow{}, false},
		"pg_stat_file not allowed":     {fakeRow{err: errors.New("permission denied for function pg_stat_file")}, false},
	}
	for name, c := range cases {
		err := checkHbaLoaded(context.Background(), fakeQuerier{c.row})
		if (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok %v", name, err, c.ok)
		}
		if err != nil && strings.Contains(err.Error(), "permission") {
			t.Errorf("%s: the server's error text reached the message", name)
		}
		if c.row.changed == &yes && (err == nil || !strings.Contains(err.Error(), "wait a second")) {
			t.Errorf("%s: %v; a reload in the same second as the edit still reads as changed", name, err)
		}
	}
}

// isolate points libpq's defaults at a temp folder, so no test reads the real passfile.
func isolate(t *testing.T) string {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", filepath.Join(home, "no-admin-passfile"))
	for _, k := range []string{"PGPASSWORD", "PGSERVICE", "PGSERVICEFILE", "PGHOST", "PGPORT", "PGUSER"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

func closedPort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestRunTouchesNoFileBeforeTheChecksPass(t *testing.T) {
	dir := isolate(t)
	old := time.Now().Add(-time.Hour)
	names := []string{tempPrefix + "123", ".pgpass-123", ".pgpass-old-456", ".pgpass-work", ".pgpass-x"}
	for _, name := range names {
		writeAged(t, filepath.Join(dir, name), "dummy\n", old)
	}
	o := options{passfile: filepath.Join(dir, ".pgpass-x"), role: "benchwatch", database: "benchwatch",
		host: "127.0.0.1", port: closedPort(t)}
	if err := run(context.Background(), o); err == nil {
		t.Fatal("a run with no server succeeded")
	}
	for _, name := range names {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(b) != "dummy\n" {
			t.Errorf("%s changed by a run that failed its checks", name)
		}
	}
}

func TestRunRefusesTheMainPassfile(t *testing.T) {
	home := isolate(t)
	main := filepath.Join(home, ".pgpass")
	admin := filepath.Join(home, "admin-pass")
	writeAged(t, main, "dummy\n", time.Now())
	writeAged(t, admin, "dummy\n", time.Now())
	t.Setenv("PGPASSFILE", admin)
	for _, target := range []string{main, admin, filepath.Join(home, ".", ".pgpass")} {
		o := options{passfile: target, role: "benchwatch", database: "benchwatch", host: "127.0.0.1",
			port: closedPort(t)}
		err := run(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "main libpq password file") {
			t.Errorf("%s: %v, want a refusal", filepath.Base(target), err)
		}
	}
	for _, f := range []string{main, admin} {
		if b, err := os.ReadFile(f); err != nil || string(b) != "dummy\n" {
			t.Errorf("%s changed", filepath.Base(f))
		}
	}
}

func TestRunRefusesTheMainPassfileBeforeItExists(t *testing.T) {
	home := isolate(t)
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	t.Setenv("HOME", link)
	t.Setenv("PGPASSFILE", "admin-pass")
	for _, target := range []string{filepath.Join(home, ".pgpass"), filepath.Join(home, ".PGPASS"),
		filepath.Join(home, "admin-pass"), "admin-pass"} {
		o := options{passfile: target, role: "benchwatch", database: "benchwatch", host: "127.0.0.1",
			port: closedPort(t)}
		err := run(context.Background(), o)
		if err == nil || !strings.Contains(err.Error(), "main libpq password file") {
			t.Errorf("%s: %v, want a refusal", target, err)
		}
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Errorf("home holds %d entries after refused runs, %v", len(entries), err)
	}
}

func TestUsageLinesFit(t *testing.T) {
	for i, line := range strings.Split(usage, "\n") {
		if len(line) > 120 {
			t.Errorf("usage line %d has %d characters", i+1, len(line))
		}
	}
}
