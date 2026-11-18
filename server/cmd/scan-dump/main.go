// Command scan-dump scans the transcripts into a shard cache and writes every data.json table to a file of its own.
// It prints counts and error codes only.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
)

const tempMaxAgeMs = 60 * 1000

var tempFile = regexp.MustCompile(`\.[0-9a-f]{16}\.tmp$`)

var errorNames = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENOSPC: "ENOSPC",
	syscall.ELOOP: "ELOOP", syscall.EEXIST: "EEXIST", syscall.ENOTDIR: "ENOTDIR", syscall.EISDIR: "EISDIR",
	syscall.EMFILE: "EMFILE", syscall.ENFILE: "ENFILE", syscall.EIO: "EIO", syscall.EROFS: "EROFS",
	syscall.EBUSY: "EBUSY", syscall.EDQUOT: "EDQUOT", syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.EXDEV: "EXDEV",
}

var codeShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,40}$`)

// codeOf names an error without its message, which can hold a path.
func codeOf(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		if name, ok := errorNames[errno]; ok {
			return name
		}
		return "ERRNO"
	}
	for _, known := range []error{scan.ErrLockLost, scan.ErrNotRegular, scan.ErrTooLarge, aggregate.ErrTooManyRows, errRefused, errUsage, errWebDir} {
		if errors.Is(err, known) && codeShape.MatchString(known.Error()) {
			return known.Error()
		}
	}
	return "ERROR"
}

var (
	errRefused = errors.New("EREFUSED")
	errUsage   = errors.New("EUSAGE")
	errWebDir  = errors.New("EWEBDIR")
)

// realPath resolves links in the longest part of path that exists, so a link cannot smuggle a write elsewhere.
func realPath(path string) string {
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// within compares case-insensitively, as the Mac's file system does.
func within(path, dir string) bool {
	p, d := strings.ToLower(realPath(path)), strings.ToLower(realPath(dir))
	return p == d || strings.HasPrefix(p, strings.TrimSuffix(d, "/")+"/")
}

type options struct {
	out, cache, projects string
	quiet                bool
	stdout               io.Writer
}

func refuse(o options) error {
	home, _ := os.UserHomeDir()
	live := filepath.Join(home, "Library/Application Support/benchwatch")
	web := os.Getenv("BENCHWATCH_WEB_DIR")
	if web == "" || !filepath.IsAbs(web) {
		return errWebDir
	}
	for _, path := range []string{o.out, o.cache} {
		if within(path, live) || within(path, web) || within(path, o.projects) || within(o.projects, path) {
			return errRefused
		}
	}
	return nil
}

func sweepTemps(dir, prefix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !tempFile.MatchString(name) || !strings.HasPrefix(name, prefix) {
			continue
		}
		file := filepath.Join(dir, name)
		var st syscall.Stat_t
		if syscall.Lstat(file, &st) == nil && float64(time.Now().UnixMilli())-scan.MsFromTimespec(st.Mtimespec) > tempMaxAgeMs {
			_ = os.Remove(file)
		}
	}
}

type outputState struct {
	Version    int    `json:"version"`
	Bytes      int64  `json:"bytes"`
	Hash       string `json:"hash"`
	Unreadable int    `json:"unreadable"`
	Unrebuilt  int    `json:"unrebuilt"`
}

func readOutputState(o options) (outputState, bool) {
	data, err := os.ReadFile(scan.OutputStateFile(o.cache))
	if err != nil {
		return outputState{}, false
	}
	parsed, err := extract.Parse(data)
	if err != nil {
		return outputState{}, false
	}
	get := func(k string) float64 { f, _ := extract.Get(parsed, k).(float64); return f }
	hash, _ := extract.Get(parsed, "hash").(string)
	state := outputState{int(get("version")), int64(get("bytes")), hash, int(get("unreadable")), int(get("unrebuilt"))}
	_, numeric := extract.Get(parsed, "unreadable").(float64)
	ok := numeric && get("version") == aggregate.OutputVersion && scan.FileMatches(o.out, state.Bytes, state.Hash)
	return state, ok
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// outputSize counts UTF-16 units, the unit of the 256 MiB string limit the output must stay under.
func outputSize(d *aggregate.Data) (int, error) {
	text, err := encode(d)
	return labels.UTF16Len(string(text)), err
}

func rssMB() int64 {
	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
	return usage.Maxrss / 1e6
}

func dump(o options, lock *scan.Lock) error {
	started := time.Now()
	store, err := scan.UpdateShards(o.projects, o.cache, lock.Beat)
	if err != nil {
		return err
	}
	state, current := readOutputState(o)
	if store.Changed == 0 && store.Removed == 0 && current && state.Unreadable == store.Unreadable && state.Unrebuilt == 0 {
		if !o.quiet {
			fmt.Fprintf(o.stdout, "no transcript changed (%d checked, %d ms, %d shard reads, max rss %d MB)\n",
				store.Checked(), time.Since(started).Milliseconds(), store.ShardReads, rssMB())
		}
		return nil
	}
	data, err := aggregate.AggregateToFit(store.Shards, aggregate.MaxRunItems, aggregate.MaxOutputChars, outputSize)
	if err != nil {
		return err
	}
	if err := store.Save(); err != nil {
		return err
	}
	unreadable, unrebuilt := store.Unreadable, len(store.Failed)
	data.Stats.Unreadable, data.Stats.Unrebuilt = &unreadable, &unrebuilt
	data.GeneratedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	data.Scan = map[string]any{
		"ms": time.Since(started).Milliseconds(), "changed": store.Changed, "removed": store.Removed,
		"rebuilt": store.Rebuilt, "bytesRead": store.BytesRead,
	}
	text, err := encode(data)
	if err != nil {
		return err
	}
	if err := scan.WriteAtomic(o.out, text, 0o600); err != nil {
		return err
	}
	stateText, err := encode(outputState{aggregate.OutputVersion, int64(len(text)), extract.HashKey(text), unreadable, unrebuilt})
	if err != nil {
		return err
	}
	if err := scan.WriteAtomic(scan.OutputStateFile(o.cache), stateText, 0o600); err != nil {
		return err
	}
	if !o.quiet {
		fmt.Fprintf(o.stdout, "%d changed, %d removed, %d rebuilt, %d MB read, %d calls, %d ms, max rss %d MB\n",
			store.Changed, store.Removed, store.Rebuilt, store.BytesRead/1e6, data.Stats.UniqueCalls,
			time.Since(started).Milliseconds(), rssMB())
	}
	return nil
}

func writeStatus(o options, ok bool, code any) error {
	text, err := encode(map[string]any{"lastRunAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "ok": ok, "code": code})
	if err != nil {
		return err
	}
	return scan.WriteAtomic(filepath.Join(o.cache, "status.json"), text, 0o600)
}

func run(o options) error {
	if err := refuse(o); err != nil {
		return err
	}
	if err := os.MkdirAll(o.cache, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(o.cache, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(o.out), 0o700); err != nil {
		return err
	}
	lock := scan.NewLock(filepath.Join(o.cache, "lock"), scan.BeatMs)
	taken, err := lock.Take(nil)
	if err != nil {
		return err
	}
	if !taken {
		if !o.quiet {
			fmt.Fprintln(o.stdout, "another scan is running; nothing to do")
		}
		return writeStatus(o, true, "LOCKED")
	}
	defer lock.Release()
	sweepTemps(filepath.Dir(o.out), filepath.Base(o.out)+".")
	sweepTemps(o.cache, "")
	if err := dump(o, lock); err != nil {
		return err
	}
	return writeStatus(o, true, nil)
}

func parse(args []string) (options, error) {
	home, _ := os.UserHomeDir()
	projects := os.Getenv("BENCHWATCH_PROJECTS")
	if projects == "" {
		projects = filepath.Join(home, ".claude/projects")
	}
	set := flag.NewFlagSet("scan-dump", flag.ContinueOnError)
	o := options{stdout: os.Stdout}
	set.StringVar(&o.projects, "projects", projects, "transcript root")
	set.BoolVar(&o.quiet, "quiet", false, "print nothing on success")
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	if err := set.Parse(args); err != nil {
		return o, errUsage
	}
	if set.NArg() != 2 {
		set.Usage()
		return o, errUsage
	}
	for _, p := range []struct {
		dst *string
		src string
	}{{&o.out, set.Arg(0)}, {&o.cache, set.Arg(1)}, {&o.projects, o.projects}} {
		abs, err := filepath.Abs(p.src)
		if err != nil {
			return o, err
		}
		*p.dst = abs
	}
	return o, nil
}

// runAndRecord leaves no status after a refusal, since the status would land in the wrong place.
func runAndRecord(o options) error {
	err := run(o)
	if err != nil && !errors.Is(err, errRefused) && !errors.Is(err, errWebDir) {
		_ = writeStatus(o, false, codeOf(err))
	}
	return err
}

func main() {
	o, err := parse(os.Args[1:])
	if err == nil {
		err = runAndRecord(o)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan failed: %s\n", codeOf(err))
		os.Exit(1)
	}
}
