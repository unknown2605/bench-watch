package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnOutputFolderThatCannotBeCreatedStillRecordsAStatus(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	blocker := filepath.Join(b.dir, "blocker")
	must(t, os.WriteFile(blocker, []byte("x"), 0o600))
	r := b.runWith(func(o *options) { o.out = filepath.Join(blocker, "data.json") })
	fatalf(t, r.err != nil, "the run must fail")
	status := readJSON(t, filepath.Join(b.cache, "status.json"))
	fatalf(t, status["ok"] == false && status["code"] == codeOf(r.err), "status %v", status)
}

func TestABadFlagPrintsOnlyTheUsageCode(t *testing.T) {
	b := sandbox(t)
	c := b.runChild("-no-such-flag")
	equal(t, c.exit, 1, "exit code")
	equal(t, c.stderr, "scan failed: EUSAGE\n", "stderr")
	equal(t, c.stdout, "", "stdout")
}
