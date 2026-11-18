// Package copilot reads GitHub Copilot CLI session logs (CM-00016): only per-request usage records become call
// events. Built from a secondary source (tokscale); no real record was seen.
package copilot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

// NanoAiuPerUSD converts totalNanoAiu: one AI credit is $0.01 and 1e9 nano-AIU.
const NanoAiuPerUSD = 1e11

const eventsFile = "events.jsonl"

var sessionDir = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Reader reads <home>/session-state/<session>/events.jsonl only: session-store.db copies the same turns, and
// config.json and token files next to it stay unopened.
type Reader struct {
	home string
	root string
}

var _ source.Reader = (*Reader)(nil)

// Home is ~/.copilot; COPILOT_HOME is not followed, as its effect is unverified.
func Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".copilot"), nil
}

func New(home string) *Reader {
	if a, err := filepath.Abs(home); err == nil {
		home = a
	}
	return &Reader{home: home, root: filepath.Join(home, "session-state")}
}

func (r *Reader) Client() source.Client { return clients.CopilotCLI }

func (r *Reader) Roots() []string { return []string{r.root} }

func (r *Reader) Detect() bool {
	st, err := os.Stat(r.root)
	return err == nil && st.IsDir()
}

func (r *Reader) Discover() (source.Discovery, error) {
	var d source.Discovery
	entries, err := os.ReadDir(r.root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			d.UnreadableDirs++
		}
		return d, nil
	}
	for _, e := range entries {
		if !e.IsDir() || !sessionDir.MatchString(e.Name()) {
			continue
		}
		full := filepath.Join(r.root, e.Name(), eventsFile)
		rel, err := filepath.Rel(r.home, full)
		if err != nil {
			continue
		}
		key := source.Key(clients.CopilotCLI, rel)
		var st syscall.Stat_t
		if err := syscall.Lstat(full, &st); err != nil {
			if !errors.Is(err, syscall.ENOENT) {
				d.UnreadableKeys = append(d.UnreadableKeys, key)
			}
			continue
		}
		if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
			continue
		}
		d.Units = append(d.Units, source.Unit{Client: clients.CopilotCLI, Key: key, Path: full, Rel: rel,
			Size: st.Size, MtimeMs: scan.MsFromTimespec(st.Mtimespec), BirthMs: scan.MsFromTimespec(st.Birthtimespec)})
	}
	return d, nil
}

// Identify takes the session and cwd from the first record, a session.start; the folder name is the session when
// that record has no id.
func (r *Reader) Identify(unit source.Unit, previous *source.Identity) (source.Identity, error) {
	if previous != nil {
		return *previous, nil
	}
	id := source.Identity{Project: "other", Session: source.SessionLabel(filepath.Base(filepath.Dir(unit.Path))),
		Scope: "main"}
	f, _, err := source.Open(r.Roots(), unit.Path)
	if err != nil {
		return source.Identity{}, err
	}
	defer f.Close()
	line, err := bufio.NewReader(io.LimitReader(f, scan.MaxLineBytes+1)).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return source.Identity{}, err
	}
	// A stored identity is never recomputed, so a first record not yet complete or of another type is retried.
	first, err := extract.Parse(bytes.TrimSuffix(line, []byte("\n")))
	if err != nil {
		return source.Identity{}, fmt.Errorf("copilot-cli: first record unreadable: %w", err)
	}
	if extract.Get(first, "type") != "session.start" {
		return source.Identity{}, fmt.Errorf("%w: first record is no session.start", source.ErrUnrecognised)
	}
	data := extract.Get(first, "data")
	if s, ok := extract.Get(data, "sessionId").(string); ok && s != "" {
		id.Session = source.SessionLabel(s)
	}
	if cwd, ok := extract.Get(extract.Get(data, "context"), "cwd").(string); ok && filepath.IsAbs(cwd) {
		id.Project = labels.ProjectLabel(source.Dashed(cwd), labels.HomePrefix())
	}
	return id, nil
}

func (r *Reader) Read(f *os.File, unit source.Unit, from source.Cursor, opt source.Options) (source.Cursor, error) {
	c := source.NewCalls(from, opt.MaxItems)
	fallback := min(extract.MaxTs, max(extract.MinTs, int64(math.Floor(unit.MtimeMs/1000))))
	apply := func(line []byte) {
		if line != nil && len(line) == 0 {
			return
		}
		c.Cur.Lines++
		record, err := extract.Parse(line)
		if _, ok := record.(map[string]any); line == nil || err != nil || !ok {
			c.Cur.Skipped++
			c.Unknown = true
			return
		}
		classify(c, record)
		usageRecord(c, record, fallback)
	}
	tail, consumed, err := source.Lines(io.NewSectionReader(f, from.Offset, math.MaxInt64-from.Offset),
		scan.MaxLineBytes, opt.Tick, apply)
	if err != nil {
		return source.Cursor{}, err
	}
	c.Cur.Offset = from.Offset + consumed
	// A last line without its newline is taken only when it parses whole; otherwise it is still being written.
	if len(tail) > 0 {
		if _, err := extract.Parse(tail); err == nil {
			apply(tail)
			c.Cur.Offset += int64(len(tail))
		}
	}
	if err := source.Unrecognised(unit, from, &c.Cur, c.Unknown); err != nil {
		return source.Cursor{}, err
	}
	return c.Cur, nil
}

// classify marks an event of a known family; session.start is the header. The families come from third-party
// parsers, as Copilot CLI is closed source.
func classify(c *source.Calls, record any) {
	kind, _ := extract.Get(record, "type").(string)
	switch family, _, _ := strings.Cut(kind, "."); {
	case kind == "session.start":
	case strings.Contains(kind, ".") && slices.Contains([]string{"session", "user", "assistant", "tool"}, family):
		c.Cur.MarkKnown()
	default:
		c.Unknown = true
	}
}

// usageRecord reads session.usage_record only: session.shutdown repeats the same tokens as a cumulative total per
// run, and a resumed session appends another one. inputTokens is taken to include the cache reads and writes.
func usageRecord(c *source.Calls, record any, fallback int64) {
	ts := fallback
	stamp := extract.Get(record, "timestamp")
	if t, ok := source.Ts(stamp); ok {
		c.Cur.LastTs, ts = t, t
	} else if c.Cur.LastTs != 0 {
		ts = c.Cur.LastTs
	}
	if extract.Get(record, "type") != "session.usage_record" {
		return
	}
	if source.BadTs(stamp) {
		c.Cur.Skipped++
	}
	u, ok := extract.Get(extract.Get(record, "data"), "usage").(map[string]any)
	id, isString := extract.Get(record, "id").(string)
	if !ok || !isString || id == "" {
		c.Cur.Skipped++
		return
	}
	var t source.Tokens
	read, write := t.Count(u["cacheReadTokens"]), t.Count(u["cacheWriteTokens"])
	input := t.Count(u["inputTokens"])
	cached := min(read+write, input)
	read = min(read, cached)
	n := [5]int64{input - cached, cached - read, 0, read, t.Count(u["outputTokens"])}
	callID := extract.HashID("copilot-cli:event:" + id)
	model := labels.Model(clients.CopilotCLI, u["model"])
	nano, isNumber := extract.Get(u["copilotUsage"], "totalNanoAiu").(float64)
	if !isNumber || math.IsInf(nano, 0) || nano != nano || nano < 0 {
		c.Add(callID, ts, model, n, t)
		return
	}
	c.AddRecorded(callID, ts, model, n, t, nano/NanoAiuPerUSD)
}
