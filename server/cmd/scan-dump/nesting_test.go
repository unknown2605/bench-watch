package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nestedTimestampLine(depth int) string {
	return `{"type":"assistant","timestamp":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) +
		`,"message":{"id":"deep","usage":{"input_tokens":1}}}`
}

func TestAn8MBNestedTimestampUnderTheCapIsSkippedAndTheRunCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an 8 MB line")
	}
	b := sandbox(t)
	file := b.write(mainRel, call("m1", 1, nil))
	appendBytes(t, file, []byte(nestedTimestampLine(4_000_000)+"\n"))
	appendTo(t, file, call("m2", 2, nil))
	s := b.scan()
	equal(t, s.data.stat("skipped"), 1, "skipped lines")
	equal(t, s.data.stat("uniqueCalls"), 2, "calls around the deep line")
	equal(t, s.status["ok"], any(true), "status")
	_, err := os.Stat(filepath.Join(b.cache, "lock"))
	fatalf(t, os.IsNotExist(err), "lock not released")
}
