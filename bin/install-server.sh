#!/bin/bash
set -euo pipefail
umask 077
CALLER_PATH="${PATH-}"
# Only go comes from the caller's PATH, so a container cannot supply any other command. The override is for tests.
export PATH="${BENCHWATCH_SYSTEM_PATH:-/usr/bin:/bin:/usr/sbin:/sbin}"
# Host paths the compose files bind writable, colon-separated; named volumes stay inside Docker's VM.
# Required: an unset list would silently skip the check, so "none" is the explicit way out.
ROOTS=()
case "${BENCHWATCH_CONTAINER_PATHS-}" in
  none) echo "BENCHWATCH_CONTAINER_PATHS=none: no container-writable paths are checked" >&2 ;;
  *$'\n'*)
    echo "refusing: BENCHWATCH_CONTAINER_PATHS holds a newline; separate the paths with colons" >&2
    exit 2 ;;
  *)
    IFS=: read -r -a RAW_ROOTS <<< "${BENCHWATCH_CONTAINER_PATHS-}" || true
    for ROOT in ${RAW_ROOTS[@]+"${RAW_ROOTS[@]}"}; do
      [ -n "$ROOT" ] || continue
      if [ -z "${ROOT//\//}" ]; then
        echo "refusing: BENCHWATCH_CONTAINER_PATHS lists / , which would cover the whole disk; list the folders" >&2
        exit 2
      fi
      case "$ROOT" in /*) ;; *) ROOT="$PWD/$ROOT" ;; esac
      # Inode tests: spellings like /. or a link to / pass the text test; the Data volume reaches the disk by firmlinks.
      if [ "$ROOT" -ef / ] || [ "$ROOT" -ef /System/Volumes/Data ]; then
        echo "refusing: BENCHWATCH_CONTAINER_PATHS lists / , which would cover the whole disk; list the folders" >&2
        exit 2
      fi
      ROOTS+=("$ROOT")
    done
    if [ "${#ROOTS[@]}" = 0 ]; then
      echo "refusing: BENCHWATCH_CONTAINER_PATHS is unset or lists no path. Set it to the host paths containers" \
        "can write, colon-separated, or to exactly none to skip the check" >&2
      exit 2
    fi ;;
esac
MAX_LINK_HOPS=32

# launchd runs this code on the host, so neither the code nor its tools may sit where a container can write.
# By inode: APFS ignores case and /System/Volumes/Data aliases /Users.
# $1 is the path to test, $2 the writable root.
in_root() (
  CDPATH=''
  dir="$1"
  root="$2"
  case "$dir" in /*) ;; *) dir="$PWD/$dir" ;; esac
  up="$dir"
  until [ "$up" = / ] || [ -z "$up" ]; do
    if [ "$up" -ef "$root" ]; then exit 0; fi
    up="${up%/*}"
  done
  until cd -P -- "$dir" 2>/dev/null; do
    case "$dir" in '' | /) exit 0 ;; esac
    dir="${dir%/*}"
    [ -n "$dir" ] || dir=/
  done
  until [ . -ef / ]; do
    if [ . -ef "$root" ]; then exit 0; fi
    cd -P -- .. 2>/dev/null || exit 0
  done
  exit 1
)
writable_at() {
  local root
  WHERE=""
  for root in ${ROOTS[@]+"${ROOTS[@]}"}; do
    if in_root "$1" "$root"; then WHERE="$root"; return 0; fi
  done
  return 1
}
refuse_where() {
  if [ -n "$WHERE" ] && [ "$WHERE" != links ]; then
    echo "refusing: $1 resolves inside $WHERE, which containers can write${2-}" >&2
  else
    echo "refusing: $1 goes through too many links, or one that cannot be read" >&2
  fi
  exit 2
}
# One hop at a time: realpath names only where a chain ends, and a link stored in www can choose that end.
# A missing part is checked like any other and left for realpath to fail on, so a dangling link gets no pass.
walk_hops() {
  local todo="$1" here=/ part next target hops=0
  case "$todo" in /*) ;; *) todo="$PWD/$todo" ;; esac
  while [ -n "$todo" ]; do
    case "$todo" in
      */*) part="${todo%%/*}"; todo="${todo#*/}" ;;
      *) part="$todo"; todo="" ;;
    esac
    case "$part" in
      '' | .) continue ;;
      ..) here="${here%/*}"; here="${here:-/}"; continue ;;
    esac
    next="${here%/}/$part"
    if writable_at "$next"; then return 1; fi
    if [ -L "$next" ]; then
      hops=$((hops + 1))
      WHERE=links
      if [ "$hops" -gt "$MAX_LINK_HOPS" ]; then return 1; fi
      target="$(readlink -n -- "$next" && printf x)" || return 1
      target="${target%x}"
      case "$target" in /*) here=/ ;; esac
      todo="$target/$todo"
    else
      here="$next"
    fi
  done
}
# bash runs a file the kernel rejects as a script unless its first line holds a NUL, so testing for #! alone misses one.
text_script() {
  local hex
  hex="$(head -c 80 -- "$1" 2>/dev/null | od -An -v -tx1 | tr -d ' \n')"
  case "$hex" in 2321*) return 0 ;; esac
  while [ -n "$hex" ]; do
    case "$hex" in 00*) return 1 ;; 0a*) return 0 ;; esac
    hex="${hex#??}"
  done
  return 0
}
# A second name can sit where containers write and rewrite this same file, and the path checks never see that name.
# Returns 0 when found, 1 when not, 2 when find failed: a failed check must not read as a passed one.
has_other_name() {
  local out
  out="$(find "$1" -type f -links +1 -print -quit 2>/dev/null)" || return 2
  [ -n "$out" ]
}
# -ef is false against a missing path, so every check against a missing root would pass.
# Any existing path counts: Docker can bind a single file, and can create a missing bind source as a folder.
require_root() {
  if [ ! -e "$1" ]; then
    echo "refusing: $1 does not exist, so the check for paths containers can write cannot be trusted" >&2
    exit 2
  fi
}
# The x keeps a trailing newline, which $() would strip: a name of two dots and a newline would become the parent.
resolve() {
  RESOLVED="$(realpath -- "$1" 2>/dev/null && printf x)" || return 1
  RESOLVED="${RESOLVED%x}"
  RESOLVED="${RESOLVED%$'\n'}"
}
# Checks a binary the job runs: no container-writable folder on the way, a real readable file with one name, and
# compiled, not a script. Sets TRUSTED to its real path.
trusted_binary() {
  local what="$1" path="$2" links=0
  if ! walk_hops "$path"; then refuse_where "$what"; fi
  if ! resolve "$path" || [ ! -f "$RESOLVED" ] || [ ! -x "$RESOLVED" ]; then
    echo "refusing: $what could not be resolved to a real executable file" >&2
    exit 2
  fi
  TRUSTED="$RESOLVED"
  if writable_at "$TRUSTED"; then refuse_where "$what"; fi
  if [ ! -r "$TRUSTED" ]; then
    echo "refusing: $what cannot be read, so it could not be checked and was not run" >&2
    exit 2
  fi
  has_other_name "$TRUSTED" || links=$?
  if [ "$links" = 2 ]; then
    echo "refusing: $what could not be checked for another hard link, so it was not run" >&2
    exit 2
  fi
  if [ "$links" = 0 ]; then
    echo "refusing: $what has another hard link, which containers may be able to write, so it was not run" >&2
    exit 2
  fi
  if text_script "$TRUSTED"; then
    echo "refusing: $what is a text script, not a compiled binary, so it was not run" >&2
    exit 2
  fi
}

LABEL=com.benchwatch.server
SOURCE="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)"
BASE="$HOME/Library/Application Support/benchwatch/server"
CACHE="$BASE/cache"
BIN="$BASE/benchwatch"
PASSFILE="$BASE/pgpass"
LOG="$BASE/launchd.log"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"
TARGET="$DOMAIN/$LABEL"
STATUS_URL="http://127.0.0.1:8790/api/status"
WAIT_SECONDS="${BENCHWATCH_INSTALL_WAIT:-120}"
# Prints 1 to 5 digits in base 10, since $(( )) reads a leading 0 as octal and refuses 08; fails on anything else.
whole_seconds() {
  case "$1" in '' | *[!0-9]* | ??????*) return 1 ;; esac
  echo "$((10#$1))"
}

if [ "$#" -gt 0 ]; then
  echo "usage: bin/install-server.sh (it takes no arguments)" >&2
  exit 2
fi
if ! WAIT_SECONDS="$(whole_seconds "$WAIT_SECONDS")"; then
  echo "refusing: BENCHWATCH_INSTALL_WAIT must be a whole number of seconds below 100000" >&2
  exit 2
fi

for ROOT in ${ROOTS[@]+"${ROOTS[@]}"}; do require_root "$ROOT"; done
if writable_at "$SOURCE"; then refuse_where "the source" "; move it out first"; fi
for TOP in bin server; do
  if [ -L "$SOURCE/$TOP" ] || [ ! -d "$SOURCE/$TOP" ]; then
    echo "refusing: $TOP/ is missing, not a folder or a symbolic link, so nothing was built" >&2
    exit 2
  fi
done
# Tests real access, so ACLs and other owners count: find can exit 0 and hide a link in a folder it cannot enter.
FOUND="$(find "$SOURCE/bin" "$SOURCE/server" -type d ! -exec test -r {} -a -x {} \; -print -quit 2>/dev/null)" || {
  echo "refusing: bin/ or server/ could not be checked for folders that cannot be searched, so nothing was built" >&2
  exit 2
}
if [ -n "$FOUND" ]; then
  echo "refusing: bin/ or server/ holds a folder that cannot be read and searched; fix its mode or owner" >&2
  exit 2
fi
FOUND="$(find "$SOURCE/bin" "$SOURCE/server" \( -type l -o -type f -links +1 \) -print -quit 2>/dev/null)" || {
  echo "refusing: bin/ or server/ could not be searched for links, so nothing was built" >&2
  exit 2
}
if [ -n "$FOUND" ]; then
  echo "refusing: bin/ or server/ holds a symbolic or hard link, and the host builds and runs that code" >&2
  exit 2
fi

# Before the cd: an empty or relative entry counts from the caller's folder.
GO_ON_PATH="$(PATH="$CALLER_PATH"; type -P go || true)"
if [ -z "$GO_ON_PATH" ]; then
  echo "refusing: go must be on PATH (go builds the server)" >&2
  exit 2
fi
trusted_binary "go on PATH" "$GO_ON_PATH"
GO="$TRUSTED"
cd "$SOURCE"

# go starts with an empty environment plus the folders below, so no caller variable (GOCACHEPROG, GOTMPDIR, CC, a
# go env file) chooses what builds or runs during the build; each folder Go reads is checked like the go binary.
goenv() {
  (cd "$SOURCE/server" && env -i HOME="$HOME" PATH="$PATH" TMPDIR="$GO_TMPDIR" ${GOPATH:+"GOPATH=$GOPATH"} \
    ${GOMODCACHE:+"GOMODCACHE=$GOMODCACHE"} ${GOCACHE:+"GOCACHE=$GOCACHE"} GOENV=off GOTOOLCHAIN=local GOPROXY=off \
    GOFLAGS="-mod=readonly${GO_MODFILE:+ -modfile=$GO_MODFILE}" GOWORK=off "$GO" "$@")
}
GO_MODFILE=""
GO_TMPDIR="${TMPDIR:-/tmp}"
case "$GO_TMPDIR" in
  /*) ;;
  *) echo "refusing: TMPDIR is not an absolute path, so the folder Go builds in could not be checked" >&2; exit 2 ;;
esac
if ! walk_hops "$GO_TMPDIR"; then refuse_where "the temporary folder (TMPDIR)"; fi
if writable_at "$GO_TMPDIR"; then refuse_where "the temporary folder (TMPDIR)"; fi
for KEY in GOROOT GOMODCACHE GOCACHE; do
  VALUE="$(goenv env "$KEY" 2>/dev/null)" || VALUE=""
  case "$VALUE" in
    /*) ;;
    *) echo "refusing: go did not name its $KEY folder, so it could not be checked" >&2; exit 2 ;;
  esac
  if ! walk_hops "$VALUE"; then refuse_where "Go's $KEY folder"; fi
  if writable_at "$VALUE"; then refuse_where "Go's $KEY folder"; fi
done
for GO_MODFILE in "" tools.mod; do
  if ! goenv mod verify >/dev/null 2>&1; then
    echo "refusing: go mod verify (${GO_MODFILE:-go.mod}) found a cached module that does not match its sums," \
      "or could not check offline" >&2
    exit 2
  fi
done
GO_MODFILE=""

WORK=""
KEEP_WORK=0
PHASE=idle
WAS_LOADED=0
BOOTOUT_SENT=0
BOOTSTRAP_SENT=0

# 0 loaded, 1 not loaded (launchctl's 113, no such service), 2 launchd could not be asked.
loaded() {
  local rc=0
  launchctl print "$TARGET" >/dev/null 2>&1 || rc=$?
  case "$rc" in 0) return 0 ;; 113) return 1 ;; *) return 2 ;; esac
}
wait_unloaded() {
  local attempt state
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    state=0
    loaded || state=$?
    if [ "$state" = 1 ]; then return 0; fi
    sleep 1
  done
  return 1
}

# What is reported comes from launchd, not from a flag: a signal can land between a call and the line after it.
recover() {
  local state=0 restored=1
  if [ "$BOOTOUT_SENT" = 1 ]; then wait_unloaded; fi
  loaded || state=$?
  if [ "$BOOTSTRAP_SENT" = 1 ] && [ "$state" = 0 ]; then
    echo "the new server is loaded, but its status was not confirmed ok. Check: launchctl print $TARGET" >&2
    return
  fi
  if [ "$state" = 2 ]; then
    KEEP_WORK=1
    echo "FAILED: launchd could not be asked whether $LABEL is loaded; nothing was put back." \
      "The previous plist and binary, if any, are in $WORK" >&2
    return
  fi
  if [ -e "$WORK/previous.plist" ]; then mv -f "$WORK/previous.plist" "$PLIST" || restored=0
  else rm -f "$PLIST" || restored=0; fi
  if [ -e "$WORK/previous.bin" ]; then mv -f "$WORK/previous.bin" "$BIN" || restored=0; fi
  if [ "$restored" = 0 ]; then
    KEEP_WORK=1
    echo "FAILED: the previous plist or binary could not be put back; what is left of them is in $WORK" >&2
    return
  fi
  if [ "$state" = 0 ]; then
    echo "the previous server is still loaded and the previous plist is back" >&2
  elif [ "$WAS_LOADED" = 0 ]; then
    echo "no server is loaded, as before" >&2
  elif launchctl bootstrap "$DOMAIN" "$PLIST"; then
    echo "the previous plist is back and the previous server was loaded again" >&2
  else
    echo "FAILED: $LABEL is not loaded; run bin/install-server.sh again" >&2
  fi
}

finish() {
  local status=$?
  set +e
  trap '' INT TERM HUP
  if [ "$PHASE" = swap ]; then
    recover
  elif [ "$PHASE" = wait ] && [ "$status" -gt 128 ]; then
    echo "interrupted: the new server is loaded, but its status was not confirmed ok" >&2
  fi
  if [ -n "$WORK" ] && [ "$KEEP_WORK" = 0 ]; then rm -rf "$WORK"; fi
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

mkdir -p "$CACHE" "$(dirname "$PLIST")"
chmod 700 "$BASE" "$CACHE"
WORK="$(mktemp -d "$BASE/install.XXXXXX")"

# Only modules already in the cache: a build never fetches code.
gobuild() { goenv build -trimpath "$@"; }
if ! gobuild -o "$WORK/benchwatch" ./cmd/benchwatch || ! gobuild -o "$WORK/bw-db-role" ./cmd/bw-db-role ||
  ! GO_MODFILE=tools.mod gobuild -o "$WORK/goose" github.com/pressly/goose/v3/cmd/goose; then
  echo "FAILED: the server did not build (modules come from the local cache only); nothing was changed" >&2
  exit 1
fi

# The administrator connection uses the caller's libpq settings, else the shared stack's documented local default.
if [ ! -e "$PASSFILE" ]; then
  if [ -z "${PGPASSWORD-}" ] && [ -z "${PGPASSFILE-}" ]; then export PGPASSWORD=root; fi
  if ! PGUSER="${PGUSER:-postgres}" "$WORK/bw-db-role" -passfile "$PASSFILE"; then
    echo "FAILED: the database role and database could not be created (the line above has the reason)" >&2
    exit 1
  fi
fi
unset PGPASSWORD
# -allow-missing applies every migration not yet applied, a lower number landing after a higher one included.
if ! env -u PGUSER -u PGDATABASE -u PGSERVICE PGPASSFILE="$PASSFILE" "$WORK/goose" -allow-missing \
  -dir "$SOURCE/server/db/migrations" postgres \
  "host=127.0.0.1 port=5432 dbname=benchwatch user=benchwatch sslmode=require" up >"$WORK/goose.log" 2>&1; then
  echo "FAILED: the migrations did not apply; the running server, if any, was not touched" >&2
  # Only migration names and codes: the rest of goose's output can quote SQL or the connection.
  grep -oE '[0-9]{5}_[a-z0-9_]+\.sql|SQLSTATE [0-9A-Z]{5}|connection refused|timeout' "$WORK/goose.log" 2>/dev/null |
    sort -u | sed 's/^/  goose: /' >&2 || true
  exit 1
fi

xml() { printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }
cat > "$WORK/plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml "$BIN")</string>
    <string>-passfile</string><string>$(xml "$PASSFILE")</string>
    <string>-cache</string><string>$(xml "$CACHE")</string>
  </array>
  <key>WorkingDirectory</key><string>$(xml "$BASE")</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>Nice</key><integer>5</integer>
  <key>StandardOutPath</key><string>/dev/null</string>
  <key>StandardErrorPath</key><string>$(xml "$LOG")</string>
</dict>
</plist>
EOF
chmod 644 "$WORK/plist"
if ! plutil -lint -s "$WORK/plist" >/dev/null 2>&1; then
  echo "FAILED: the generated plist is not valid (plutil -lint); nothing was changed" >&2
  exit 1
fi

if [ -e "$PLIST" ]; then cp -p "$PLIST" "$WORK/previous.plist"; fi
STATE=0
loaded || STATE=$?
if [ "$STATE" = 2 ]; then
  echo "FAILED: launchd could not be asked whether $LABEL is loaded; nothing was changed" >&2
  exit 1
fi
if [ "$STATE" = 0 ]; then WAS_LOADED=1; fi
PHASE=swap
if [ "$WAS_LOADED" = 1 ]; then
  BOOTOUT_SENT=1
  launchctl bootout "$TARGET" 2>/dev/null || true
fi
if ! wait_unloaded; then
  BOOTOUT_SENT=0
  echo "FAILED: the previous server would not unload, so the new one was not loaded" >&2
  exit 1
fi
if [ -e "$BIN" ]; then mv "$BIN" "$WORK/previous.bin"; fi
mv "$WORK/benchwatch" "$BIN"
mv "$WORK/plist" "$PLIST"
BOOTSTRAP_SENT=1
if ! launchctl bootstrap "$DOMAIN" "$PLIST"; then
  echo "FAILED: launchctl could not load the new server" >&2
  exit 1
fi
PHASE=wait

# Ready: the database answers with its schema, history is not paused, the machine collector has a sample, and the
# answer comes from the agent's own process, not from whatever else may listen on 8790.
field() { plutil -extract "$1" raw -o - "$WORK/status.json" 2>/dev/null || true; }
agent_pid() { launchctl print "$TARGET" 2>/dev/null | sed -n 's/^[[:space:]]*pid = \([0-9][0-9]*\)$/\1/p' | head -n 1; }
# Exactly one listening process, else none: a second one could be the process that answered.
listener_pid() {
  local pids
  pids="$(lsof -nP -iTCP@127.0.0.1:8790 -sTCP:LISTEN -t 2>/dev/null || true)"
  case "$pids" in '' | *[!0-9]*) ;; *) echo "$pids" ;; esac
}
deadline=$(( $(date +%s) + WAIT_SECONDS ))
ready=0
while [ "$ready" = 0 ] && [ "$(date +%s)" -le "$deadline" ]; do
  curl -q -sf --max-time 2 -H 'Host: 127.0.0.1:8790' -o "$WORK/status.json" "$STATUS_URL" 2>/dev/null ||
    : > "$WORK/status.json"
  pid="$(agent_pid)"
  if [ "$(field database.state)" = ok ] && [ "$(field database.historyPaused)" = false ] &&
    [ "$(field machine.state)" = ok ] && [ -n "$pid" ] && [ "$pid" = "$(listener_pid)" ]; then
    ready=1
  else
    sleep 1
  fi
done
if [ "$ready" = 0 ]; then
  echo "FAILED: the server is loaded, but its status did not read ok within $WAIT_SECONDS s" >&2
  echo "Check: launchctl print $TARGET" >&2
  exit 1
fi
PHASE=done

echo "installed $LABEL on 127.0.0.1:8790; database role, migrations and agent are in place"
echo "confirmed: the agent's own process answers /api/status with the database, history and machine collector ok"
