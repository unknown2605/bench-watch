#!/usr/bin/env bash
# Fixture test of bw-db-role and the migrations on the shared PostgreSQL 17; drops only the database and role it made.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
db=benchwatch_cm00002_test
role=benchwatch_cm00002_test
spare_db=benchwatch_cm00002_test_nope
spare_role=benchwatch_cm00002_test_nope
loose=cm00002_test_unconfined
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly
export PGHOST=127.0.0.1 PGPORT=5432 PGSSLMODE=require PGCONNECT_TIMEOUT=10
unset PGUSER PGPASSWORD PGPASSFILE PGDATABASE PGSERVICE

# Local-dev default superuser of the shared stack (local-stack skill), used only for setup and teardown.
admin() { PGUSER=postgres PGPASSWORD=root PGDATABASE=postgres psql -X -q -v ON_ERROR_STOP=1 -At "$@"; }

failures=0
pass() { printf 'PASS  %s\n' "$1"; }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }
expect() { if [ "$2" = "$3" ]; then pass "$1 ($2)"; else fail "$1 (got $2, want $3)"; fi; }
# Checks a captured output against every password recorded so far before anything prints it; grep exit 2 counts.
screen() {
  local rc=0
  grep -qFf "$tmp/secrets" "$1" || rc=$?
  if [ "$rc" -ne 1 ]; then
    fail "$(basename "$1") held a password or could not be checked; output withheld"
    echo "[withheld]" >"$1"
  fi
}
show() { screen "$1"; cat "$1"; }

present() {
  admin -c "SELECT (SELECT count(*) FROM pg_database WHERE datname IN ('$db', '$spare_db', '$loose'))
    || ' ' || (SELECT count(*) FROM pg_roles WHERE rolname IN ('$role', '$spare_role', '$loose'))"
}
others() {
  admin -c "SELECT string_agg(datname || ':' || datdba::regrole || ':' || coalesce(datacl::text, '-'), ','
    ORDER BY datname)
    FROM pg_database WHERE datname NOT IN ('$db', '$spare_db', '$loose')" | shasum -a 256 | cut -c1-16
  admin -c "SELECT string_agg(rolname || ':' || rolsuper || rolcreatedb || rolcreaterole || rolcanlogin
    || ':' || rolconnlimit || ':' || coalesce(rolvaliduntil::text, '-'), ',' ORDER BY rolname)
    FROM pg_roles WHERE rolname NOT IN ('$role', '$spare_role', '$loose')" | shasum -a 256 | cut -c1-16
}

echo "== before"
before="$(present)"
echo "fixture databases present: ${before% *}, fixture roles present: ${before#* }"
if [ "$before" != "0 0" ]; then
  echo "refusing to run: a fixture name already exists, and this script drops only what it created" >&2
  exit 1
fi
others_before="$(others)"
echo "fingerprint of every other database and role: $(echo "$others_before" | tr '\n' ' ')"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/cm00002-db.XXXXXX")"
chmod 700 "$tmp"
passfile="$tmp/pgpass"
# Every password this run creates, kept in a private file so grep reads it with -f, never from its argv.
(umask 077; : >"$tmp/secrets")
started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

cleanup() {
  status=$?
  set +e
  echo "== cleanup"
  admin -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "DROP DATABASE IF EXISTS $spare_db WITH (FORCE)" \
    -c "DROP DATABASE IF EXISTS $loose WITH (FORCE)" \
    -c "DROP ROLE IF EXISTS $role" -c "DROP ROLE IF EXISTS $spare_role" -c "DROP ROLE IF EXISTS $loose"
  rm -rf "$tmp"
  after="$(present)"
  echo "fixture databases present: ${after% *}, fixture roles present: ${after#* }"
  [ -e "$tmp" ] && echo "temp folder still present" || echo "temp folder and passfile removed"
  others_after="$(others)"
  if [ "$others_after" = "$others_before" ]; then
    echo "other databases and roles unchanged (fingerprint matches)"
  else
    echo "other databases or roles CHANGED" >&2
    status=1
  fi
  [ "$after" = "0 0" ] && [ ! -e "$tmp" ] || status=1
  exit "$status"
}
trap cleanup EXIT

cd "$root/server"
go build -o "$tmp/bw-db-role" ./cmd/bw-db-role
# The same runner as bin/install-server.sh: -allow-missing applies a lower-numbered file that lands after a higher one.
goose() { go tool -modfile=tools.mod goose -allow-missing -dir "${goose_dir:-db/migrations}" postgres "$@"; }
as_role() { env PGUSER="$role" PGPASSFILE="$passfile" PGDATABASE="$db" psql -X -q -w -v ON_ERROR_STOP=1 -At "$@"; }
any_db() { env PGUSER="$role" PGPASSFILE="$tmp/pgpass-any" psql -X -q -w -v ON_ERROR_STOP=1 -At "$@"; }

helper() { PGUSER=postgres PGPASSWORD=root "$tmp/bw-db-role" "$@"; }
refuses() {
  local label="$1" out="$2"
  shift 2
  if helper "$@" >"$out" 2>&1; then screen "$out"; fail "$label: it ran"
  else screen "$out"; pass "$label: $(cat "$out")"; fi
}

echo "== role helper"
helper -passfile "$passfile" -database "$db" -role "$role" >"$tmp/out1" 2>&1
(umask 077; cut -d: -f5 "$passfile" >>"$tmp/secrets")
show "$tmp/out1"
expect "passfile mode" "$(stat -f %Lp "$passfile")" 600
expect "passfile lines" "$(wc -l <"$passfile" | tr -d ' ')" 1
expect "passfile target" "$(cut -d: -f1-4 "$passfile")" "127.0.0.1:5432:$db:$role"
first_sum="$(shasum -a 256 "$passfile" | cut -c1-64)"
expect "role carries the bw-db-role marker" \
  "$(admin -c "SELECT shobj_description('$role'::regrole, 'pg_authid')")" "benchwatch bw-db-role"

expect "role attributes super,createdb,createrole,login,replication,bypassrls,inherit" \
  "$(admin -c "SELECT concat_ws(',', rolsuper, rolcreatedb, rolcreaterole, rolcanlogin, rolreplication, rolbypassrls,
    rolinherit) FROM pg_roles WHERE rolname = '$role'")" "f,f,f,t,f,f,f"
expect "stored secret is a SCRAM-SHA-256 verifier" \
  "$(admin -c "SELECT rolpassword LIKE 'SCRAM-SHA-256\$4096:%' FROM pg_authid WHERE rolname = '$role'")" t
expect "databases owned by the role" \
  "$(admin -c "SELECT string_agg(datname, ',') FROM pg_database WHERE datdba = '$role'::regrole")" "$db"
expect "PUBLIC may connect to $db" "$(admin -c "SELECT has_database_privilege('public', '$db', 'CONNECT')")" f
expect "objects owned by the role outside $db" \
  "$(admin -c "SELECT count(*) FROM pg_shdepend WHERE refobjid = '$role'::regrole AND deptype = 'o'
    AND dbid <> (SELECT oid FROM pg_database WHERE datname = '$db') AND classid <> 'pg_database'::regclass")" 0

helper -passfile "$passfile" -database "$db" -role "$role" >"$tmp/out2" 2>&1
(umask 077; cut -d: -f5 "$passfile" >>"$tmp/secrets")
show "$tmp/out2"
second_sum="$(shasum -a 256 "$passfile" | cut -c1-64)"
[ "$second_sum" != "$first_sum" ] && pass "second run rotates the password" || fail "second run rotates the password"
expect "passwords recorded for the leak checks" "$(sort -u "$tmp/secrets" | wc -l | tr -d ' ')" 2
expect "files beside the passfile after two runs (no backup of an old password)" \
  "$(find "$tmp" -maxdepth 1 -name '.pgpass-*' | wc -l | tr -d ' ')" 0

# A privilege on another database is a shared dependency (dbid 0); the helper must count it and refuse.
admin -c "CREATE DATABASE $spare_db" -c "GRANT CONNECT ON DATABASE $spare_db TO $role"
refuses "refuses a role holding a privilege on another database" "$tmp/out7" \
  -passfile "$passfile" -database "$db" -role "$role"
expect "refused run left the passfile as it was" "$(shasum -a 256 "$passfile" | cut -c1-64)" "$second_sum"

# Old temp files beside the passfile go only once every check has passed, and only the helper's own: its prefix and
# one line for a confined role. Earlier versions' names (.pgpass-111, a dated backup) stay; tmp-444 has other content.
pf="$tmp/pf"
mkdir -m 700 "$pf"
own=.pgpass-bw-db-role-tmp
for f in .pgpass-work .pgpass-x .pgpass-111 .pgpass-old-222 .pgpass-20261109 $own-333 $own-444; do
  case $f in
    .pgpass-111 | .pgpass-old-222 | .pgpass-20261109 | $own-333) line="127.0.0.1:5432:$db:$role:x" ;;
    *) line=dummy ;;
  esac
  (umask 077; echo "$line" >"$pf/$f")
  touch -t 202601010000 "$pf/$f"
done
listing() { ls -A "$pf" | LC_ALL=C sort | tr '\n' ' '; }
refuses "refuses with old temp files beside a passfile named .pgpass-x" "$tmp/outA" \
  -passfile "$pf/.pgpass-x" -database "$db" -role "$role"
expect "files beside the refused run's passfile" "$(listing)" \
  ".pgpass-111 .pgpass-20261109 $own-333 $own-444 .pgpass-old-222 .pgpass-work .pgpass-x "
expect "their content" \
  "$(cat "$pf/.pgpass-111" "$pf/.pgpass-old-222" "$pf/.pgpass-20261109" "$pf/$own-333" "$pf/$own-444" |
    cut -d: -f4- | sort -u | tr '\n' ' ')" \
  "$role:x dummy "
admin -c "DROP DATABASE $spare_db"
helper -passfile "$pf/.pgpass-x" -database "$db" -role "$role" >"$tmp/outB" 2>&1
(umask 077; cut -d: -f5 "$pf/.pgpass-x" >>"$tmp/secrets")
show "$tmp/outB"
expect "files left once the checks passed" "$(listing)" \
  ".pgpass-111 .pgpass-20261109 $own-444 .pgpass-old-222 .pgpass-work .pgpass-x "
expect "the dated backup unchanged" "$(cut -d: -f4- "$pf/.pgpass-20261109")" "$role:x"
expect ".pgpass-work unchanged" "$(cat "$pf/.pgpass-work")" dummy
(umask 077; cp "$pf/.pgpass-x" "$passfile")

admin -c "CREATE ROLE $spare_role LOGIN"
refuses "refuses a role it did not create" "$tmp/out5" -passfile "$tmp/spare" -database "$spare_db" -role "$spare_role"
expect "refused run left the role without a password and created no database" "$(admin -c "SELECT
  (SELECT rolpassword IS NULL FROM pg_authid WHERE rolname = '$spare_role')
  || ',' || (SELECT count(*) FROM pg_database WHERE datname = '$spare_db')")" "true,0"
[ -e "$tmp/spare" ] && fail "refused run wrote no passfile" || pass "refused run wrote no passfile"
admin -c "DROP ROLE $spare_role" -c "CREATE DATABASE $spare_db"
refuses "refuses a database without its role" "$tmp/out6" \
  -passfile "$tmp/spare" -database "$spare_db" -role "$spare_role"
expect "refused run created no role" "$(admin -c "SELECT count(*) FROM pg_roles WHERE rolname = '$spare_role'")" 0
admin -c "DROP DATABASE $spare_db"
refuses "refuses role postgres" "$tmp/out3" -passfile "$tmp/other" -database postgres -role postgres
refuses "refuses database postgres" "$tmp/out4" -passfile "$tmp/other" -database postgres -role "$role"
refuses "refuses a database named unlike its role (pg_hba sameuser)" "$tmp/out8" \
  -passfile "$tmp/other" -database "$spare_db" -role "$role"
refuses "refuses a name the pg_hba lines do not confine" "$tmp/out9" \
  -passfile "$tmp/other" -database "$loose" -role "$loose"
[ -e "$tmp/other" ] && fail "refused runs wrote no passfile" || pass "refused runs wrote no passfile"
expect "fixture databases and roles after the refused runs" "$(present)" "1 1"

echo "== login with the passfile"
expect "login as the role over TLS" \
  "$(as_role -c "SELECT current_user || ',' || ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()")" "$role,true"

echo "== migrations"
dsn="host=127.0.0.1 port=5432 dbname=$db user=$role sslmode=require"
export PGPASSFILE="$passfile"

echo "== 00009 client column over stored rows"
goose "$dsn" up-to 8 >/dev/null
as_role -c "INSERT INTO usage_daily VALUES (current_date, 'benchwatch', 'main', 'claude-opus-5-5',
  1, 0, 0, 0, 0, 0, 0, 0, 0, 0)" \
  -c "INSERT INTO context_band_daily VALUES (current_date, 'benchwatch', 'main', 0, 1000, 1, 0, 0)" \
  -c "INSERT INTO session_daily VALUES (current_date, 'benchwatch', '0a1b2c3d', 'main', 'claude-opus-5-5',
  1, 0, 0, 0, 0, 0, 0, 0, 0, now(), now())"
client_rows="SELECT (SELECT count(*) || ':' || string_agg(DISTINCT client::text, ',') FROM usage_daily) || ' ' ||
  (SELECT count(*) || ':' || string_agg(DISTINCT client::text, ',') FROM context_band_daily) || ' ' ||
  (SELECT count(*) || ':' || string_agg(DISTINCT client::text, ',') FROM session_daily)"
client_keys="SELECT count(*) FROM pg_constraint WHERE contype = 'p' AND pg_get_constraintdef(oid) LIKE '%, client)'
  AND conrelid::regclass::text IN ('usage_daily', 'context_band_daily', 'session_daily')"
goose "$dsn" up-to 9 >/dev/null
expect "stored rows become claude-code" "$(as_role -c "$client_rows")" "1:claude-code 1:claude-code 1:claude-code"
expect "client is the last primary key column of the three tables" "$(as_role -c "$client_keys")" 3
as_role -c "INSERT INTO usage_daily VALUES (current_date, 'benchwatch', 'main', 'claude-opus-5-5',
  1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'codex')" \
  -c "INSERT INTO usage_daily VALUES (current_date, 'benchwatch', 'main', 'gpt-5.1-codex-max',
  1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'codex')"
expect "same key under another client is a new row" "$(as_role -c "SELECT count(*) FROM usage_daily")" 3
for bad in "a/b codex usage_daily_model_check" "gpt.says.hi/x codex usage_daily_model_check" \
  "gpt-5 cursor client_id"; do
  read -r model client reason <<<"$bad"
  if as_role -c "INSERT INTO usage_daily VALUES (current_date, 'benchwatch', 'main', '$model',
    1, 0, 0, 0, 0, 0, 0, 0, 0, 0, '$client')" 2>"$tmp/err"; then fail "00009 refuses $model under $client"
  elif grep -q "$reason" "$tmp/err"; then pass "00009 refuses $model under $client: $reason"
  else fail "00009 refuses $model under $client for another reason: $(head -1 "$tmp/err")"; fi
done
if as_role -c "INSERT INTO usage_daily VALUES (current_date, 'benchwatch', 'main', 'gpt says hi',
  1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'codex')" 2>"$tmp/err"; then fail "00009 refuses text as a model"
elif grep -q usage_daily_model_check "$tmp/err"; then pass "00009 refuses text as a model"
else fail "00009 refuses text as a model for another reason: $(head -1 "$tmp/err")"; fi
if goose "$dsn" down >"$tmp/out" 2>&1; then fail "00009 down refuses rows of another client"; else
  pass "00009 down refuses rows of another client: $(grep -oE '00009 down: [a-z_]+ holds [0-9]+ rows' "$tmp/out")"
fi
expect "version after the refused down" "$(as_role -c "SELECT max(version_id) FROM goose_db_version")" 9
as_role -c "DELETE FROM usage_daily WHERE client = 'codex'"
goose "$dsn" down >/dev/null
expect "client column gone after 00009 down" "$(as_role -c "SELECT count(*) FROM information_schema.columns
  WHERE table_schema = 'public' AND column_name = 'client'")" 0
expect "rows kept by 00009 down" "$(as_role -c "SELECT (SELECT count(*) FROM usage_daily) || ',' ||
  (SELECT count(*) FROM context_band_daily) || ',' || (SELECT count(*) FROM session_daily)")" "1,1,1"
expect "00001 model check back after 00009 down" "$(as_role -c "SELECT is_model_label('gpt-5')")" f
goose "$dsn" up-to 9 >/dev/null
expect "rows after 00009 up again" "$(as_role -c "$client_rows")" "1:claude-code 1:claude-code 1:claude-code"
expect "client keys after 00009 up again" "$(as_role -c "$client_keys")" 3
as_role -c "DELETE FROM usage_daily" -c "DELETE FROM context_band_daily" -c "DELETE FROM session_daily"

goose "$dsn" up
goose "$dsn" status
expect "time zone drop_expired_partitions subtracts keeps in" \
  "$(as_role -c "SELECT array_to_string(proconfig, ',') FROM pg_proc WHERE proname = 'drop_expired_partitions'")" \
  "search_path=public, pg_temp,TimeZone=UTC"
expect "partitioned tables" "$(as_role -c "SELECT count(*) FROM partition_policy")" 18
expect "missing partitions for the current and next day or month" "$(as_role -c "
  SELECT count(*) FROM partition_policy p, generate_series(0, 1) i
  WHERE to_regclass(p.parent || '_p' || to_char(date_trunc(p.period::text, now() AT TIME ZONE 'UTC')
    + i * CASE p.period WHEN 'day' THEN interval '1 day' ELSE interval '1 month' END,
    CASE p.period WHEN 'day' THEN 'YYYYMMDD' ELSE 'YYYYMM' END)) IS NULL")" 0
as_role -c "SELECT parent || ' -> ' || child FROM list_partitions()
  WHERE parent IN ('machine_samples', 'machine_rollup_1m')"
expect "ensure_partitions is idempotent" "$(as_role -c "SELECT ensure_partitions(now(), 1)")" 0
expect "partitions created for a day 30 days back" \
  "$(as_role -c "SELECT ensure_partitions(now() - interval '30 days', 0)")" \
  "$(as_role -c "SELECT 7 + CASE WHEN date_trunc('month', now() AT TIME ZONE 'UTC')
    = date_trunc('month', (now() - interval '30 days') AT TIME ZONE 'UTC') THEN 0 ELSE 11 END")"
expect "expired raw partitions dropped under 14-day retention" \
  "$(as_role -c "SELECT count(*) FROM drop_expired_partitions(now())")" 7
expect "partitions left" "$(as_role -c "SELECT count(*) FROM list_partitions()")" \
  "$(as_role -c "SELECT 36 + CASE WHEN date_trunc('month', now() AT TIME ZONE 'UTC')
    = date_trunc('month', (now() - interval '30 days') AT TIME ZONE 'UTC') THEN 0 ELSE 11 END")"

echo "== read and write own tables"
as_role -c "INSERT INTO usage_daily VALUES (current_date, 'benchwatch', 'main', 'claude-opus-5-5',
  3, 10, 20, 30, 40, 50, 0.1, 0.2, 0.3, 0.4)"
as_role -c "INSERT INTO machine_samples VALUES (now(), 12.5, 1, 2, 3, 100, 50, 30, 10, 5, 0, 0, 'normal', 7, 8)"
expect "rows read back" "$(as_role -c "SELECT (SELECT count(*) FROM usage_daily) || ',' ||
  (SELECT count(*) FROM machine_samples)")" "1,1"
as_role -c "DELETE FROM usage_daily" -c "DELETE FROM machine_samples"
expect "rows after delete" "$(as_role -c "SELECT (SELECT count(*) FROM usage_daily) || ',' ||
  (SELECT count(*) FROM machine_samples)")" "0,0"
if as_role -c "INSERT INTO usage_daily VALUES (current_date, '/Users/x/project', 'main', 'claude-opus-5-5',
  0, 0, 0, 0, 0, 0, 0, 0, 0, 0)" 2>"$tmp/err"; then fail "label check refuses a path"; else
  pass "label check refuses a path: $(grep -o 'violates check constraint "[a-z_]*"' "$tmp/err")"; fi
if as_role -c "INSERT INTO usage_daily VALUES (current_date, '.env', 'main', 'unknown', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)" \
  2>"$tmp/err"; then fail "label check refuses a dot name"; else pass "label check refuses a dot name"; fi

echo "== scanner sentinels are stored"
as_role -c "INSERT INTO tool_result_daily VALUES (current_date, 'other', 'main', 'Read: image', 1, 1, 1, 0, 1, 0)"
as_role -c "INSERT INTO session_daily VALUES (current_date, 'home', 'unknown', 'main', 'unknown',
  1, 0, 0, 0, 0, 0, 0, 0,
  0, now(), now())"
as_role -c "INSERT INTO compaction_events VALUES ('1762560000', now(), current_date, 'scratch (tmp)', 'unknown', 'sub',
  'auto', 10, NULL)"
expect "sentinel rows stored" "$(as_role -c "SELECT (SELECT count(*) FROM tool_result_daily) || ',' ||
  (SELECT count(*) FROM session_daily) || ',' || (SELECT count(*) FROM compaction_events)")" "1,1,1"
as_role -c "DELETE FROM tool_result_daily" -c "DELETE FROM session_daily" -c "DELETE FROM compaction_events"

echo "== clear_transcript_days"
as_role -c "INSERT INTO usage_daily SELECT current_date - d, 'benchwatch', 'main', 'unknown', 1, 0, 0, 0, 0, 0,
  0, 0, 0, 0 FROM generate_series(1, 3) d"
expect "rows cleared when day 1 is rebuilt and days 2 and 3 are gone from the transcripts" \
  "$(as_role -c "SELECT clear_transcript_days(ARRAY[current_date - 1])")" 1
expect "rows cleared by a scan that rebuilt nothing" "$(as_role -c "SELECT clear_transcript_days('{}')")" 0
expect "days kept" \
  "$(as_role -c "SELECT string_agg((current_date - day)::text, ',' ORDER BY day DESC) FROM usage_daily")" 2,3
as_role -c "DELETE FROM usage_daily"

echo "== privilege limits"
if as_role -c "CREATE DATABASE $spare_db" 2>"$tmp/err"; then fail "role cannot create a database"; else
  pass "role cannot create a database: $(head -1 "$tmp/err")"; fi
if as_role -c "CREATE ROLE $spare_role" 2>"$tmp/err"; then fail "role cannot create a role"; else
  pass "role cannot create a role: $(head -1 "$tmp/err")"; fi
# The fixture passfile names one database; this copy names any, so only the server decides. pg_hba rejects the
# monitor's roles everywhere but their own database (K1); PUBLIC keeps CONNECT on the other databases by decision.
(umask 077; sed 's/^\([^:]*:[^:]*:\)[^:]*:/\1*:/' "$passfile" >"$tmp/pgpass-any")
reached=0
tried=0
while IFS= read -r other; do
  [ -n "$other" ] || continue
  tried=$((tried + 1))
  if any_db -d "$other" -c "SELECT 1" >/dev/null 2>"$tmp/err"; then
    reached=$((reached + 1))
    echo "  reached $other"
  elif ! grep -q 'pg_hba.conf rejects connection' "$tmp/err"; then
    reached=$((reached + 1))
    echo "  $other: refused for another reason: $(head -1 "$tmp/err")"
  fi
done < <(admin -c "SELECT datname FROM pg_database WHERE datallowconn AND datname <> '$db' ORDER BY datname")
[ "$tried" -gt 0 ] && pass "connection attempts to other databases ($tried)" || fail "no other database to try"
expect "other databases the role reached (each attempt must be rejected by pg_hba)" "$reached" 0
expect "role still reaches its own database" "$(any_db -d "$db" -c "SELECT current_database()")" "$db"

echo "== Go tests against the fixture database"
db_pkgs=(internal/store internal/history internal/transcripts internal/guardlog internal/server)
db_tests="$(for p in "${db_pkgs[@]}"; do cat "$p"/*_test.go; done | grep -c '^func Test')"
if CM_TEST_DSN="$dsn" go test -count=1 -p 1 -v "${db_pkgs[@]/#/./}" >"$tmp/gotest" 2>&1; then
  expect "database package tests passed (${db_pkgs[*]})" "$(grep -c '^--- PASS' "$tmp/gotest")" "$db_tests"
  expect "database package tests skipped" "$(grep -c '^--- SKIP' "$tmp/gotest" || true)" 0
  grep -E '^    [a-z_]+_test\.go:[0-9]+: retention (before|restored)' "$tmp/gotest" || true
else
  fail "database package tests"
  show "$tmp/gotest"
fi

echo "== migrations down and up again"
goose "$dsn" down-to 0
expect "tables left after down-to 0" "$(as_role -c "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'
  AND tablename <> 'goose_db_version'")" 0
expect "types left after down-to 0" "$(as_role -c "SELECT count(*) FROM pg_type t
  JOIN pg_namespace n ON n.oid = t.typnamespace
  WHERE n.nspname = 'public' AND t.typtype = 'e'")" 0
expect "functions left after down-to 0" "$(as_role -c "SELECT count(*) FROM pg_proc p
  JOIN pg_namespace n ON n.oid = p.pronamespace
  WHERE n.nspname = 'public'")" 0
goose "$dsn" up >/dev/null
expect "partitions after second up" "$(as_role -c "SELECT count(*) FROM list_partitions()")" 36

echo "== migrations applied out of order"
mig="$tmp/migrations"
mkdir "$mig"
cp db/migrations/*.sql "$mig"/
probe() {
  printf -- '-- +goose Up\nCREATE TABLE %s (id integer);\n-- +goose Down\nDROP TABLE %s;\n' "$2" "$2" >"$mig/$1"
}
probe 90002_probe_high.sql probe_high
goose_dir="$mig" goose "$dsn" up >/dev/null
probe 90001_probe_low.sql probe_low
goose_dir="$mig" goose "$dsn" up >/dev/null
expect "lower-numbered migration that arrived later is applied" \
  "$(as_role -c "SELECT to_regclass('probe_low') IS NOT NULL AND to_regclass('probe_high') IS NOT NULL")" t
expect "probe versions in the order applied" "$(as_role -c "SELECT string_agg(version_id::text, ',' ORDER BY id)
  FROM goose_db_version WHERE version_id >= 90000")" "90002,90001"
pending="$(goose_dir="$mig" goose "$dsn" status 2>&1 | grep -c Pending || true)"
expect "pending migrations after the second up" "$pending" 0
goose_dir="$mig" goose "$dsn" down >/dev/null
goose_dir="$mig" goose "$dsn" down >/dev/null
expect "probe tables after two downs" \
  "$(as_role -c "SELECT count(*) FROM pg_tables WHERE tablename IN ('probe_low', 'probe_high')")" 0

echo "== password never shown"
cat "$tmp"/out[0-9A-Z] "$tmp/gotest" >"$tmp/outputs"
leaks="$(grep -cFf "$tmp/secrets" "$tmp/outputs" || true)"
expect "helper output lines holding a password" "$leaks" 0
if [ -z "${BENCHWATCH_PG_CONTAINER-}" ]; then
  echo "SKIP  PostgreSQL log checks: BENCHWATCH_PG_CONTAINER (the Postgres container name) is not set"
elif docker logs --since "$started" "$BENCHWATCH_PG_CONTAINER" >"$tmp/pglog" 2>&1; then
  pass "PostgreSQL log read ($(wc -l <"$tmp/pglog" | tr -d ' ') lines)"
  expect "PostgreSQL log lines holding a password" "$(grep -cFf "$tmp/secrets" "$tmp/pglog" || true)" 0
  expect "PostgreSQL log lines holding a verifier or PASSWORD clause" \
    "$(grep -cE -e 'SCRAM-SHA-256\$' -e "PASSWORD '" "$tmp/pglog" || true)" 0
else
  fail "PostgreSQL log could not be read, so the leak check did not run"
fi

echo "== result: $failures failure(s)"
[ "$failures" -eq 0 ]
