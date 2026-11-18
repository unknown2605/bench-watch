// Package gemini reads Gemini CLI chat recordings and Qwen Code's fork of them (CM-00016): only model replies that
// carry token counts become call events. Built from upstream source; no real record of either was seen.
package gemini

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
	"strings"
	"syscall"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

// maxLegacy bounds a legacy whole-file .json session, which is parsed in one piece.
const maxLegacy = 16 << 20

var errLegacyTooLarge = errors.New("legacy session over the size cap")

var (
	geminiMain = regexp.MustCompile(`^session-[A-Za-z0-9_-]{1,160}\.jsonl?$`)
	subagentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}\.jsonl$`)
	plainDir   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,255}$`)
	shortID    = regexp.MustCompile(`-([0-9a-f]{8})\.jsonl?$`)
)

// Reader reads one Gemini CLI or Qwen Code home; the two differ in layout and record shape only.
type Reader struct {
	client clients.ID
	home   string
	roots  []string
}

var _ source.Reader = (*Reader)(nil)

func abs(dir string) string {
	if a, err := filepath.Abs(dir); err == nil {
		return a
	}
	return dir
}

func userDir(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, name), nil
}

// GeminiHome is ~/.gemini; GEMINI_CLI_HOME is not followed, as its effect is unverified.
func GeminiHome() (string, error) { return userDir(".gemini") }

// QwenHome is ~/.qwen; QWEN_HOME and QWEN_RUNTIME_DIR are not followed, as their effect is unverified.
func QwenHome() (string, error) { return userDir(".qwen") }

// NewGemini reads tmp/<project>/chats only, so oauth_creds.json, google_accounts.json and settings stay unopened.
func NewGemini(home string) *Reader {
	home = abs(home)
	return &Reader{client: clients.GeminiCLI, home: home, roots: []string{filepath.Join(home, "tmp")}}
}

// NewQwen reads projects/<cwd>/chats and the older tmp/<hash>/chats only.
func NewQwen(home string) *Reader {
	home = abs(home)
	return &Reader{client: clients.QwenCode, home: home,
		roots: []string{filepath.Join(home, "projects"), filepath.Join(home, "tmp")}}
}

func (r *Reader) Client() source.Client { return r.client }

func (r *Reader) Roots() []string { return append([]string(nil), r.roots...) }

func (r *Reader) Detect() bool {
	for _, root := range r.roots {
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func list(dir string, d *source.Discovery) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			d.UnreadableDirs++
		}
		return nil
	}
	return entries
}

func (r *Reader) add(d *source.Discovery, full string) {
	rel, err := filepath.Rel(r.home, full)
	if err != nil {
		return
	}
	key := source.Key(r.client, rel)
	var st syscall.Stat_t
	if err := syscall.Lstat(full, &st); err != nil {
		if !errors.Is(err, syscall.ENOENT) {
			d.UnreadableKeys = append(d.UnreadableKeys, key)
		}
		return
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return
	}
	d.Units = append(d.Units, source.Unit{Client: r.client, Key: key, Path: full, Rel: rel, Size: st.Size,
		MtimeMs: scan.MsFromTimespec(st.Mtimespec), BirthMs: scan.MsFromTimespec(st.Birthtimespec)})
}

// Discover lists <root>/<project>/chats/<file>; Gemini also nests a sub-agent's file under chats/<parent session>/.
func (r *Reader) Discover() (source.Discovery, error) {
	var d source.Discovery
	for _, root := range r.roots {
		for _, project := range list(root, &d) {
			if !project.IsDir() || !plainDir.MatchString(project.Name()) {
				continue
			}
			chats := filepath.Join(root, project.Name(), "chats")
			for _, e := range list(chats, &d) {
				name := e.Name()
				switch {
				case e.Type().IsRegular() && r.client == clients.GeminiCLI && geminiMain.MatchString(name):
					r.add(&d, filepath.Join(chats, name))
				case e.Type().IsRegular() && r.client == clients.QwenCode && subagentID.MatchString(name):
					r.add(&d, filepath.Join(chats, name))
				case e.IsDir() && r.client == clients.GeminiCLI && plainDir.MatchString(name):
					for _, sub := range list(filepath.Join(chats, name), &d) {
						if sub.Type().IsRegular() && subagentID.MatchString(sub.Name()) {
							r.add(&d, filepath.Join(chats, name, sub.Name()))
						}
					}
				}
			}
		}
	}
	return d, nil
}

// Identify files a Gemini sub-agent's calls under its parent session with main scope, since non-Claude shards
// carry no sub-agent rows; the session comes from the recording's own id, else from the file name.
func (r *Reader) Identify(unit source.Unit, previous *source.Identity) (source.Identity, error) {
	if previous != nil {
		return *previous, nil
	}
	id := source.Identity{Project: "other", Session: "unknown", Scope: "main"}
	base := filepath.Base(unit.Path)
	parent := filepath.Base(filepath.Dir(unit.Path))
	if r.client == clients.GeminiCLI && parent != "chats" {
		id.Session = source.SessionLabel(parent)
		return id, r.check(unit)
	}
	if m := shortID.FindStringSubmatch(base); m != nil {
		id.Session = m[1]
	} else if r.client == clients.QwenCode {
		id.Session = source.SessionLabel(strings.TrimSuffix(base, ".jsonl"))
	}
	f, _, err := source.Open(r.roots, unit.Path)
	if err != nil {
		return source.Identity{}, err
	}
	defer f.Close()
	// A stored identity is never recomputed, so a first record not readable yet is retried on the next scan; an
	// oversized legacy file never becomes readable and Read counts it as skipped.
	first, err := firstRecord(f, strings.HasSuffix(base, ".json"))
	if errors.Is(err, errLegacyTooLarge) {
		return id, nil
	}
	if err != nil {
		return source.Identity{}, fmt.Errorf("%s: first record unreadable: %w", r.client, err)
	}
	if s, ok := extract.Get(first, "sessionId").(string); ok && s != "" {
		id.Session = source.SessionLabel(s)
	}
	if r.client == clients.QwenCode {
		id.Project = projectOf(extract.Get(first, "cwd"))
	}
	return id, nil
}

func (r *Reader) check(unit source.Unit) error {
	if err := source.Check(r.roots, unit.Path); guardErr(err) {
		return err
	}
	return nil
}

func guardErr(err error) bool {
	return errors.Is(err, source.ErrOutsideRoots) || errors.Is(err, source.ErrForbiddenName) ||
		errors.Is(err, source.ErrRelativePath)
}

func firstRecord(f *os.File, whole bool) (any, error) {
	if whole {
		data, err := io.ReadAll(io.LimitReader(f, maxLegacy+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxLegacy {
			return nil, errLegacyTooLarge
		}
		return extract.Parse(data)
	}
	line, err := bufio.NewReader(io.LimitReader(f, scan.MaxLineBytes+1)).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	first := bytes.TrimSuffix(line, []byte("\n"))
	return extract.Parse(first)
}

// Read continues a .jsonl recording at from.Offset; a legacy .json is rewritten whole, so it is parsed whole each
// time on top of the stored events, which only ever gain counts.
func (r *Reader) Read(f *os.File, unit source.Unit, from source.Cursor, opt source.Options) (source.Cursor, error) {
	c := source.NewCalls(from, opt.MaxItems)
	fallback := clampTs(int64(math.Floor(unit.MtimeMs / 1000)))
	if strings.HasSuffix(unit.Path, ".json") {
		data, err := io.ReadAll(io.LimitReader(io.NewSectionReader(f, 0, math.MaxInt64), maxLegacy+1))
		if err != nil {
			return source.Cursor{}, err
		}
		c.Cur.Lines, c.Cur.Offset = 1, int64(len(data))
		if len(data) > maxLegacy {
			c.Cur.Skipped = 1
			return c.Cur, nil
		}
		// A rewrite caught half-written, or another shape, keeps the previous shard.
		record, perr := extract.Parse(data)
		if perr != nil {
			return source.Cursor{}, fmt.Errorf("%s: legacy session unparsable: %w", r.client, perr)
		}
		messages, ok := extract.Get(record, "messages").([]any)
		if !ok {
			return source.Cursor{}, fmt.Errorf("%w: legacy session without messages", source.ErrUnrecognised)
		}
		c.Cur.Skipped = 0
		for _, m := range messages {
			r.geminiMessage(c, m, fallback)
		}
		if err := source.Unrecognised(unit, source.Cursor{}, &c.Cur, c.Unknown); err != nil {
			return source.Cursor{}, err
		}
		return c.Cur, nil
	}
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
		if r.client == clients.QwenCode {
			r.qwenRecord(c, record, fallback)
			return
		}
		r.geminiRecord(c, record, fallback)
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

func clampTs(sec int64) int64 { return min(extract.MaxTs, max(extract.MinTs, sec)) }

func (r *Reader) stamp(c *source.Calls, v any, fallback int64) int64 {
	if ts, ok := source.Ts(v); ok {
		c.Cur.LastTs = ts
		return ts
	}
	if c.Cur.LastTs != 0 {
		return c.Cur.LastTs
	}
	return fallback
}

// geminiRecord: $rewindTo and $patch drop or edit messages whose tokens were already billed, so they change no
// count; a $set checkpoint relists messages, which their ids dedupe.
func (r *Reader) geminiRecord(c *source.Calls, record any, fallback int64) {
	if _, ok := extract.Get(record, "sessionId").(string); ok && extract.Get(record, "type") == extract.Undefined {
		return
	}
	if extract.Get(record, "$rewindTo") != extract.Undefined || extract.Get(record, "$patch") != extract.Undefined {
		c.Cur.MarkKnown()
		return
	}
	if set, ok := extract.Get(record, "$set").(map[string]any); ok {
		c.Cur.MarkKnown()
		if messages, ok := extract.Get(set, "messages").([]any); ok {
			for _, m := range messages {
				r.geminiMessage(c, m, fallback)
			}
		}
		return
	}
	r.geminiMessage(c, record, fallback)
}

// geminiMessage: tokens.input includes the cached part and excludes tool-use prompt tokens; output excludes thoughts.
func (r *Reader) geminiMessage(c *source.Calls, m any, fallback int64) {
	switch extract.Get(m, "type") {
	case "gemini":
		c.Cur.MarkKnown()
	case "user", "info", "error", "warning":
		c.Cur.MarkKnown()
		return
	default:
		c.Unknown = true
		return
	}
	tokens, ok := extract.Get(m, "tokens").(map[string]any)
	if !ok {
		return
	}
	id, ok := extract.Get(m, "id").(string)
	if !ok || id == "" {
		c.Cur.Skipped++
		return
	}
	var t source.Tokens
	n := split(t.Count(tokens["input"])+t.Count(tokens["tool"]), t.Count(tokens["cached"]),
		t.Count(tokens["output"])+t.Count(tokens["thoughts"]))
	stamp := extract.Get(m, "timestamp")
	if source.BadTs(stamp) {
		c.Cur.Skipped++
	}
	c.Add(extract.HashID("gemini-cli:message:"+id), r.stamp(c, stamp, fallback),
		modelOf(r.client, extract.Get(m, "model")), n, t)
}

// qwenRecord reads assistant records only: ui_telemetry system records repeat the same usage.
func (r *Reader) qwenRecord(c *source.Calls, record any, fallback int64) {
	stamp := extract.Get(record, "timestamp")
	ts := r.stamp(c, stamp, fallback)
	switch extract.Get(record, "type") {
	case "assistant":
		c.Cur.MarkKnown()
	case "user", "tool_result", "system":
		c.Cur.MarkKnown()
		return
	default:
		c.Unknown = true
		return
	}
	u, ok := extract.Get(record, "usageMetadata").(map[string]any)
	if !ok {
		return
	}
	id, ok := extract.Get(record, "uuid").(string)
	if !ok || id == "" {
		c.Cur.Skipped++
		return
	}
	if source.BadTs(stamp) {
		c.Cur.Skipped++
	}
	var t source.Tokens
	n := split(t.Count(u["promptTokenCount"])+t.Count(u["toolUsePromptTokenCount"]),
		t.Count(u["cachedContentTokenCount"]),
		t.Count(u["candidatesTokenCount"])+t.Count(u["thoughtsTokenCount"]))
	c.Add(extract.HashID("qwen-code:uuid:"+id), ts, modelOf(r.client, extract.Get(record, "model")), n, t)
}

func split(input, cached, output int64) [5]int64 {
	cached = min(cached, input)
	return [5]int64{input - cached, 0, 0, cached, output}
}

func modelOf(client clients.ID, v any) string { return labels.Model(client, v) }

// projectOf labels a Qwen record's cwd the way a Claude Code project folder is labelled.
func projectOf(v any) string {
	if cwd, ok := v.(string); ok && filepath.IsAbs(cwd) {
		return labels.ProjectLabel(source.Dashed(cwd), labels.HomePrefix())
	}
	return "other"
}
