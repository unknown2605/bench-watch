#!/bin/bash
set -euo pipefail
# Nothing here resolves through the caller's folders. The override is for tests.
export PATH="${BENCHWATCH_SYSTEM_PATH:-/usr/bin:/bin:/usr/sbin:/sbin}"
LABEL=com.benchwatch.server
TARGET="gui/$(id -u)/$LABEL"
BIN="$HOME/Library/Application Support/benchwatch/server/benchwatch"
if [ "$#" -gt 0 ]; then
  echo "usage: bin/uninstall-server.sh (it takes no arguments)" >&2
  exit 2
fi
launchctl bootout "$TARGET" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/$LABEL.plist"
# launchctl print exits 113 for a service it does not know; any other failure says nothing about the server.
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  rc=0
  launchctl print "$TARGET" >/dev/null 2>&1 || rc=$?
  if [ "$rc" = 113 ]; then
    rm -f "$BIN"
    echo "removed $LABEL and its binary; the database, its role, the passfile and the cache stay in place"
    exit 0
  fi
  if [ "$rc" != 0 ]; then
    echo "FAILED: launchd could not be asked whether $LABEL is loaded; the plist is gone, the binary stays." \
      "Check: launchctl print $TARGET" >&2
    exit 1
  fi
  sleep 1
done
echo "FAILED: $LABEL is still loaded; the plist is gone but the server keeps running." \
  "Try: launchctl bootout $TARGET" >&2
exit 1
