// Command bw-db-role creates or rotates the monitor's PostgreSQL role and database and writes a libpq passfile.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const scramIterations = 4096

// Set on every role the helper creates; a role without it belongs to someone else and is never changed.
const roleMarker = "benchwatch bw-db-role"

const usage = `usage: bw-db-role -passfile <path> [-database benchwatch] [-role benchwatch]
                  [-host 127.0.0.1] [-port 5432]

The role must be benchwatch or benchwatch_<suffix> and the database must have the same name, because
those are the names the shared stack's pg_hba lines confine to their own database. The helper reads the rules
from pg_hba_file_rules and refuses to run unless the two lines sit above any other host line the role could
match, and unless the pg_hba file is older than PostgreSQL's last configuration load (pg_stat_file of
hba_file against pg_conf_load_time); when that time cannot be read it refuses too. Creates the role
(LOGIN, no superuser, no CREATEDB, no CREATEROLE) and the database it owns, revokes all database privileges
from PUBLIC, sets a new random password and writes it to the passfile with mode 600. The passfile must be a
file of its own: the main libpq password file (.pgpass in $HOME or the account's home, or PGPASSFILE) is
refused, whether or not it exists yet.
Run again to rotate the password. Every check runs before any change, and an existing role is changed only
when this helper created it (its marker comment), is no superuser, has no memberships and owns nothing and
holds no privilege outside its database. The administrator connection uses the libpq environment (PGUSER,
PGPASSWORD or PGPASSFILE, PGSSLMODE) against the postgres database on -host and -port. The server only
receives a SCRAM-SHA-256 verifier, and the password is never printed or logged.
`

// The names the stack's pg_hba lines confine: `host sameuser <this> all scram-sha-256`, then `host all <this> all
// reject` (infrastructure-services). Any other name would get a role that reaches every database.
var confinedRole = regexp.MustCompile(`^benchwatch(_[a-z0-9_]+)?$`)

const maxIdentifier = 63

type options struct {
	passfile string
	database string
	role     string
	host     string
	port     int
}

func main() {
	var o options
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.StringVar(&o.passfile, "passfile", "", "path of the libpq passfile to write (required)")
	flag.StringVar(&o.database, "database", "benchwatch", "database to create and give to the role")
	flag.StringVar(&o.role, "role", "benchwatch", "role to create or rotate")
	flag.StringVar(&o.host, "host", "127.0.0.1", "PostgreSQL host, also written into the passfile")
	flag.IntVar(&o.port, "port", 5432, "PostgreSQL port, also written into the passfile")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "bw-db-role:", err)
		os.Exit(1)
	}
}

func validate(o options) error {
	if o.passfile == "" {
		return errors.New("-passfile is required")
	}
	if !confinedRole.MatchString(o.role) || len(o.role) > maxIdentifier {
		return errors.New("-role must be benchwatch or benchwatch_<lower-case letters, digits, _>, " +
			"at most 63 characters, the names pg_hba confines")
	}
	if o.database != o.role {
		return errors.New("-database must equal -role: pg_hba lets the role reach only the database of its own name")
	}
	if o.port < 1 || o.port > 65535 || o.host == "" || strings.ContainsAny(o.host, ":\\\n ") {
		return errors.New("-host or -port is not usable in a passfile")
	}
	return nil
}

// A folder others can write to would let them swap the passfile between the write and the rename.
func checkTarget(path string) error {
	dir := filepath.Dir(path)
	st, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("passfile folder: %w", err)
	}
	if !st.IsDir() {
		return errors.New("passfile folder is not a directory")
	}
	if st.Mode().Perm()&0o022 != 0 {
		return errors.New("passfile folder is writable by group or others")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != os.Getuid() {
		return errors.New("passfile folder belongs to another user")
	}
	st, err = os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("passfile: %w", err)
	}
	if !st.Mode().IsRegular() {
		return errors.New("passfile exists and is not a regular file")
	}
	return nil
}

// The helper replaces the whole file, so pointing it at the passfile libpq reads by default would drop every
// other entry in it. libpq takes the home from $HOME or the account, so both count, and a file yet to exist too.
func refuseMainPassfile(path string) error {
	var mains []string
	if home := os.Getenv("HOME"); home != "" {
		mains = append(mains, filepath.Join(home, ".pgpass"))
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		mains = append(mains, filepath.Join(u.HomeDir, ".pgpass"))
	}
	if env := os.Getenv("PGPASSFILE"); env != "" {
		mains = append(mains, env)
	}
	for _, m := range mains {
		if samePassfile(m, path) {
			return errors.New("-passfile names the main libpq password file (.pgpass in the home folder or " +
				"PGPASSFILE); give the role a passfile of its own")
		}
	}
	return nil
}

// samePassfile compares folders with links resolved and names without case, as APFS does; a doubt counts as a match.
func samePassfile(a, b string) bool {
	if sa, err := os.Stat(a); err == nil {
		if sb, err := os.Stat(b); err == nil && os.SameFile(sa, sb) {
			return true
		}
	}
	ra, okA := resolvedPath(a)
	rb, okB := resolvedPath(b)
	return !okA || !okB || strings.EqualFold(ra, rb)
}

func resolvedPath(p string) (string, bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if errors.Is(err, fs.ErrNotExist) {
		return abs, true
	}
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, filepath.Base(abs)), true
}

func newPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// The value PostgreSQL stores in pg_authid for SCRAM-SHA-256 (RFC 5802 section 3, RFC 7677).
func scramVerifier(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	storedKey := sha256.Sum256(hmacSHA256(salted, "Client Key"))
	serverKey := hmacSHA256(salted, "Server Key")
	enc := base64.StdEncoding
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, enc.EncodeToString(salt),
		enc.EncodeToString(storedKey[:]), enc.EncodeToString(serverKey)), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func passfileLine(o options, password string) string {
	escape := strings.NewReplacer(`\`, `\\`, `:`, `\:`)
	fields := []string{o.host, strconv.Itoa(o.port), o.database, o.role, password}
	for i, f := range fields {
		fields[i] = escape.Replace(f)
	}
	return strings.Join(fields, ":") + "\n"
}

// Written beside the target, so the final rename cannot cross file systems.
func writeTemp(path, content string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), tempPrefix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.WriteString(content)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// Only the SQLSTATE and server message, or the kind of a client-side failure, are kept, so no statement
// text or connection string can reach the output.
func sqlError(step string, err error) error {
	var pg *pgconn.PgError
	var netErr *net.OpError
	switch {
	case errors.As(err, &pg):
		return fmt.Errorf("%s: SQLSTATE %s: %s", step, pg.Code, pg.Message)
	case errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err):
		return fmt.Errorf("%s: timed out", step)
	case errors.As(err, &netErr):
		return fmt.Errorf("%s: network error during %s", step, netErr.Op)
	}
	return fmt.Errorf("%s failed", step)
}

type existing struct {
	roleFound   bool
	super       bool
	marked      bool
	memberships int
	foreignObjs int
	dbFound     bool
	dbOwner     string
}

// refusal applies every rule before anything changes, so a refused run leaves roles, databases and files alone.
func refusal(o options, e existing) error {
	switch {
	case !e.roleFound && e.dbFound:
		return errors.New("the database exists but the role does not; refusing to take it over")
	case e.roleFound && !e.marked:
		return errors.New("the role exists and was not created by bw-db-role; refusing to change it")
	case e.roleFound && e.super:
		return errors.New("the role is a superuser; refusing to change it")
	case e.roleFound && e.memberships > 0:
		return errors.New("the role is a member of a role or has members; refusing to change it")
	case e.roleFound && e.foreignObjs > 0:
		return errors.New("the role owns objects or holds privileges outside its database; refusing to change it")
	case e.dbFound && e.dbOwner != o.role:
		return errors.New("the database exists and belongs to another role; refusing to change it")
	}
	return nil
}

func run(ctx context.Context, o options) error {
	if err := validate(o); err != nil {
		return err
	}
	abs, err := filepath.Abs(o.passfile)
	if err != nil {
		return err
	}
	o.passfile = abs
	if err := checkTarget(o.passfile); err != nil {
		return err
	}
	if err := refuseMainPassfile(o.passfile); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d dbname=postgres", o.host, o.port))
	if err != nil {
		return errors.New("admin connection settings are not valid")
	}
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return sqlError("admin connection", err)
	}
	defer admin.Close(context.Background())

	// A failing statement that holds the verifier must not reach the server log or pg_stat_activity.
	for _, stmt := range []string{
		"SET log_statement = 'none'",
		"SET log_min_error_statement = 'panic'",
		"SET log_min_duration_statement = -1",
		"SET track_activities = off",
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			return sqlError("quiet admin session", err)
		}
	}

	e, err := inspect(ctx, admin, o)
	if err != nil {
		return err
	}
	if err := refusal(o, e); err != nil {
		return err
	}
	if err := checkHbaLoaded(ctx, admin); err != nil {
		return err
	}
	rules, err := loadHba(ctx, admin)
	if err != nil {
		return err
	}
	if err := checkHba(rules, o.role); err != nil {
		return err
	}
	n, err := removeStaleTemps(o.passfile, time.Now())
	var skipped skippedTemps
	switch {
	case errors.As(err, &skipped):
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	case err != nil:
		return errors.New("could not list the passfile folder for stale temp files")
	}
	if n > 0 {
		fmt.Printf("removed %d stale passfile temp file(s) left by an interrupted run\n", n)
	}
	if err := prepare(ctx, admin, o, e); err != nil {
		return err
	}
	if err := rotate(ctx, admin, o); err != nil {
		return err
	}
	fmt.Printf("role %s and database %s ready; passfile written with mode 600\n", o.role, o.database)
	return nil
}

func inspect(ctx context.Context, admin *pgx.Conn, o options) (existing, error) {
	var e existing
	err := admin.QueryRow(ctx, `SELECT r.rolsuper,
		coalesce(shobj_description(r.oid, 'pg_authid') = $3, false),
		(SELECT count(*) FROM pg_auth_members m WHERE m.member = r.oid OR m.roleid = r.oid)::int,
		(SELECT count(*) FROM pg_shdepend s WHERE s.refclassid = 'pg_authid'::regclass AND s.refobjid = r.oid
			AND NOT coalesce(s.classid = 'pg_database'::regclass
				AND s.objid = (SELECT oid FROM pg_database WHERE datname = $2), false)
			AND s.dbid IS DISTINCT FROM (SELECT oid FROM pg_database WHERE datname = $2))::int
			+ (SELECT count(*) FROM pg_database d WHERE d.datdba = r.oid AND d.datname <> $2)::int
		FROM pg_roles r WHERE r.rolname = $1`, o.role, o.database, roleMarker).
		Scan(&e.super, &e.marked, &e.memberships, &e.foreignObjs)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return e, sqlError("look up role", err)
	default:
		e.roleFound = true
	}
	err = admin.QueryRow(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = $1`,
		o.database).Scan(&e.dbOwner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return e, sqlError("look up database", err)
	default:
		e.dbFound = true
	}
	return e, nil
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// pg_hba_file_rules parses the file as it is now, not as loaded. Whole seconds, because pg_stat_file drops the rest.
func checkHbaLoaded(ctx context.Context, q rowQuerier) error {
	var changed bool
	err := q.QueryRow(ctx, `SELECT (pg_stat_file(current_setting('hba_file'))).modification
		>= date_trunc('second', pg_conf_load_time())`).Scan(&changed)
	switch {
	case err != nil:
		return errors.New("could not compare the pg_hba file's modification time with PostgreSQL's last " +
			"configuration load; refusing to create the role")
	case changed:
		return errors.New("the pg_hba file changed after PostgreSQL last loaded its configuration, or in the same " +
			"second; wait a second, run select pg_reload_conf() and try again")
	}
	return nil
}

type hbaRule struct {
	number                int
	kind, address, method string
	databases, users      []string
	err                   bool
}

func loadHba(ctx context.Context, admin *pgx.Conn) ([]hbaRule, error) {
	rows, err := admin.Query(ctx, `SELECT coalesce(rule_number, 0), coalesce(type, ''), coalesce(database, '{}'),
		coalesce(user_name, '{}'), coalesce(address, ''), coalesce(auth_method, ''), error IS NOT NULL
		FROM pg_hba_file_rules ORDER BY rule_number NULLS FIRST`)
	if err != nil {
		return nil, sqlError("read pg_hba rules", err)
	}
	rules, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hbaRule, error) {
		var h hbaRule
		return h, r.Scan(&h.number, &h.kind, &h.databases, &h.users, &h.address, &h.method, &h.err)
	})
	if err != nil {
		return nil, sqlError("read pg_hba rules", err)
	}
	return rules, nil
}

// checkHba wants, among the host lines the role could match, first the sameuser scram-sha-256 line and then the
// reject line for confinedRole. Socket lines reach only shells inside the container, and the role cannot replicate.
func checkHba(rules []hbaRule, role string) error {
	token := "/" + confinedRole.String()
	only := func(list []string, v string) bool { return len(list) == 1 && list[0] == v }
	sameuser := false
	for _, r := range rules {
		if r.err {
			return errors.New("pg_hba holds lines PostgreSQL could not load; fix them before creating the role")
		}
	}
	for _, r := range rules {
		if r.kind == "local" || only(r.databases, "replication") || !mayMatch(r.users, role) {
			continue
		}
		confining := r.kind == "host" && only(r.users, token) && r.address == "all"
		switch {
		case !sameuser && confining && only(r.databases, "sameuser") && r.method == "scram-sha-256":
			sameuser = true
		case sameuser && confining && only(r.databases, "all") && r.method == "reject":
			return nil
		default:
			return errors.New("PostgreSQL has not loaded the pg_hba lines that confine the role to its database " +
				"above every other host line it could match; refusing to create it")
		}
	}
	return errors.New("PostgreSQL has not loaded the two pg_hba lines that confine the role; refusing to create it")
}

// mayMatch counts a group or an unreadable pattern as a match, so a doubt refuses.
func mayMatch(users []string, role string) bool {
	for _, u := range users {
		switch {
		case u == "all" || u == role || strings.HasPrefix(u, "+") || strings.HasPrefix(u, "@"):
			return true
		case strings.HasPrefix(u, "/"):
			re, err := regexp.Compile(u[1:])
			if err != nil || re.MatchString(role) {
				return true
			}
		}
	}
	return false
}

const roleAttrs = "LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 20"

// prepare creates what is missing, without a password; a failure here leaves any existing password untouched.
func prepare(ctx context.Context, admin *pgx.Conn, o options, e existing) error {
	role := pgx.Identifier{o.role}.Sanitize()
	db := pgx.Identifier{o.database}.Sanitize()
	if !e.roleFound {
		if err := createMarkedRole(ctx, admin, role); err != nil {
			return err
		}
	}
	if !e.dbFound {
		stmt := "CREATE DATABASE " + db + " OWNER " + role + " ENCODING 'UTF8' TEMPLATE template0"
		if _, err := admin.Exec(ctx, stmt); err != nil {
			return sqlError("create database", err)
		}
	}
	for _, stmt := range []string{
		"REVOKE ALL ON DATABASE " + db + " FROM PUBLIC",
		"GRANT CONNECT, TEMPORARY ON DATABASE " + db + " TO " + role,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			return sqlError("database privileges", err)
		}
	}
	return nil
}

// One transaction, so a role without the marker, which the helper would then refuse, can never be left behind.
func createMarkedRole(ctx context.Context, admin *pgx.Conn, role string) error {
	tx, err := admin.Begin(ctx)
	if err != nil {
		return sqlError("begin", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "CREATE ROLE "+role+" WITH "+roleAttrs); err != nil {
		return sqlError("create role", err)
	}
	if _, err := tx.Exec(ctx, "COMMENT ON ROLE "+role+" IS '"+roleMarker+"'"); err != nil {
		return sqlError("mark role", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlError("create role", err)
	}
	return nil
}

// rotate commits the new password before the new passfile replaces the old one, and keeps no copy of the old
// file, so no file is ever left holding a password that still works after a later run.
func rotate(ctx context.Context, admin *pgx.Conn, o options) error {
	password, err := newPassword()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	verifier, err := scramVerifier(password, salt, scramIterations)
	if err != nil {
		return err
	}
	// ALTER ROLE takes no bind parameters; the verifier is base64 plus `$` and `:`, checked before quoting.
	if strings.ContainsAny(verifier, `'\`) {
		return errors.New("unexpected character in the verifier")
	}
	tmp, err := writeTemp(o.passfile, passfileLine(o, password))
	if err != nil {
		return fmt.Errorf("writing the passfile: %w", err)
	}
	defer os.Remove(tmp)

	tx, err := admin.Begin(ctx)
	if err != nil {
		return sqlError("begin", err)
	}
	defer tx.Rollback(context.Background())
	role := pgx.Identifier{o.role}.Sanitize()
	if _, err := tx.Exec(ctx, "ALTER ROLE "+role+" WITH "+roleAttrs+" PASSWORD '"+verifier+"'"); err != nil {
		return sqlError("set password", err)
	}
	return commitThenReplace(ctx, tx.Commit, tmp, o.passfile)
}

const commitTimeout = 30 * time.Second

// Once the commit starts, a signal no longer stops it, so a commit that lands is always followed by the rename.
func commitThenReplace(ctx context.Context, commit func(context.Context) error, tmp, target string) error {
	if ctx.Err() != nil {
		return errors.New("interrupted before the commit; the password and the passfile are unchanged")
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
	defer cancel()
	if err := commit(cctx); err != nil {
		return fmt.Errorf("%w; the passfile is unchanged, and the password may or may not have changed: "+
			"run bw-db-role again", sqlError("commit", err))
	}
	if err := os.Rename(tmp, target); err != nil {
		return errors.New("the password was changed but the passfile could not be replaced; run bw-db-role again")
	}
	return nil
}

const staleAfter = 2 * time.Minute

// Earlier versions' `.pgpass-<digits>` and `.pgpass-old-<digits>` temps are left alone: a person's dated backup can
// have the same name and content (CM-00009 Q2-L2). os.CreateTemp puts digits where writeTemp's pattern has `*`.
const tempPrefix = ".pgpass-bw-db-role-tmp-"

var ownTemp = regexp.MustCompile(`^` + regexp.QuoteMeta(tempPrefix) + `[0-9]+$`)

type skippedTemps int

func (n skippedTemps) Error() string {
	return fmt.Sprintf("could not remove %d stale passfile temp file(s); continuing", int(n))
}

// removeStaleTemps removes only files shaped like its own temps: older than a running helper's, private, one link,
// and one passfile line for a confined role. A removal that fails is skipped and counted.
func removeStaleTemps(target string, now time.Time) (int, error) {
	dir := filepath.Dir(target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed, skipped := 0, 0
	for _, e := range entries {
		if !ownTemp.MatchString(e.Name()) || e.Name() == filepath.Base(target) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		checked, ok := ownTempFile(path, now)
		if !ok {
			continue
		}
		switch err := removeChecked(path, checked); {
		case errors.Is(err, errReplaced):
		case err != nil:
			skipped++
		default:
			removed++
		}
	}
	if skipped > 0 {
		return removed, skippedTemps(skipped)
	}
	return removed, nil
}

const maxTempSize = 1 << 10

// ownTempFile returns what it checked, so the removal can tell when another file took the name meanwhile.
func ownTempFile(path string, now time.Time) (os.FileInfo, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Size() >= maxTempSize ||
		now.Sub(st.ModTime()) < staleAfter {
		return nil, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() || sys.Nlink != 1 {
		return nil, false
	}
	b := make([]byte, maxTempSize)
	n, _ := io.ReadFull(f, b)
	return st, isRoleLine(string(b[:n]))
}

var errReplaced = errors.New("another file took the temp's name")

func removeChecked(path string, checked os.FileInfo) error {
	if cur, err := os.Lstat(path); err != nil || !os.SameFile(checked, cur) {
		return errReplaced
	}
	return os.Remove(path)
}

// isRoleLine accepts exactly one passfile line, as passfileLine writes it, whose user is a confined role.
func isRoleLine(s string) bool {
	line, ok := strings.CutSuffix(s, "\n")
	if !ok || strings.ContainsAny(line, "\n\r") {
		return false
	}
	var fields []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
		case c == ':':
			fields = append(fields, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	fields = append(fields, cur.String())
	return len(fields) == 5 && confinedRole.MatchString(fields[3]) && fields[2] == fields[3] && fields[4] != ""
}
