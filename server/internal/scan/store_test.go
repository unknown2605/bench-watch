package scan

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

// Node computes birthtimeMs and mtimeMs as sec*1e3 + nsec/1e6 in doubles; a fused multiply-add would round differently.
func TestMsFromTimespecMatchesNodeStat(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	dir := t.TempDir()
	var files []string
	for i := 0; i < 200; i++ {
		file := filepath.Join(dir, fmt.Sprintf("f%03d", i))
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1_700_000_000+int64(i)*7919, int64(i)*4_999_999+123_456_789%1_000_000_000)
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	script := `const fs=require('fs');console.log(JSON.stringify(process.argv.slice(1).map(f=>{const s=fs.lstatSync(f);` +
		`return [s.mtimeMs,s.birthtimeMs]})))`
	out, err := exec.Command(node, append([]string{"-e", script}, files...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var want [][2]float64
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatal(err)
	}
	fractional := 0
	for i, file := range files {
		var st syscall.Stat_t
		if err := syscall.Lstat(file, &st); err != nil {
			t.Fatal(err)
		}
		got := [2]float64{MsFromTimespec(st.Mtimespec), MsFromTimespec(st.Birthtimespec)}
		if got != want[i] {
			t.Fatalf("file %d: go %v, node %v", i, got, want[i])
		}
		if got[0] != float64(int64(got[0])) {
			fractional++
		}
	}
	if fractional < 100 {
		t.Fatalf("only %d stamps had a fraction to round", fractional)
	}
}

func TestOwnershipBreaksTiesByBirthThenScopeThenKey(t *testing.T) {
	entries := map[string]Entry{
		"d": {FirstTs: 5, BirthMs: 1730000000000.1252},
		"c": {FirstTs: 5, BirthMs: 1730000000000.125},
		"b": {FirstTs: 5, BirthMs: 1730000000000.125, Sub: true},
		"a": {FirstTs: 5, BirthMs: 1730000000000.1252, Sub: true},
		"e": {FirstTs: 4, BirthMs: 1730000000009},
		"f": {FirstTs: 5, BirthMs: 1730000000000.125},
	}
	got := OwnershipOrder(entries)
	want := []string{"e", "c", "f", "b", "d", "a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}
