// Package codex reads Codex rollout files (CM-00016): token_count events become call events, nothing else is kept.
// Only sessions/YYYY/MM/DD/rollout-*.jsonl and archived_sessions/rollout-*.jsonl[.zst] are opened.
package codex

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/scan"
	"benchwatch/server/internal/source"
)

const (
	sessionsDir = "sessions"
	archivedDir = "archived_sessions"
	readChunk   = 1024 * 1024
	maxTokens   = 1e9
	maxWindow   = 128 << 20
	// Pending keys carry the turn's model and the last cumulative totals across a resumed read.
	pendingModel  = "model"
	pendingTotals = "totals"
)

var (
	zstdMagic    = []byte{0x28, 0xB5, 0x2F, 0xFD}
	liveName     = regexp.MustCompile(`^rollout-.*\.jsonl$`)
	archiveName  = regexp.MustCompile(`^rollout-.*\.jsonl(?:\.zst)?$`)
	threadInName = regexp.MustCompile(
		`-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl(?:\.zst)?$`)
	uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	yearDir   = regexp.MustCompile(`^[0-9]{4}$`)
	dayDir    = regexp.MustCompile(`^[0-9]{2}$`)
	// OpenAI model ids as Codex records them (gpt-5.1-codex-max, o4-mini, codex-mini-latest, gpt-4o-2024-08-06).

	// ErrNoResume is a compressed unit whose re-read gave fewer events than its stored cursor holds.
	ErrNoResume = errors.New("compressed rollout changed under its stored cursor")
)

// Reader reads one Codex home. Units are keyed by thread id, so a rollout moved into archived_sessions keeps its shard.
type Reader struct {
	home string
}

var _ source.Reader = (*Reader)(nil)

func Home() (string, error) {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Abs(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func New(home string) *Reader {
	if abs, err := filepath.Abs(home); err == nil {
		home = abs
	}
	return &Reader{home: home}
}

func (r *Reader) Client() source.Client { return clients.Codex }

// Roots holds the two rollout folders only, so auth.json, config and the sqlite files next to them are refused.
func (r *Reader) Roots() []string {
	return []string{filepath.Join(r.home, sessionsDir), filepath.Join(r.home, archivedDir)}
}

func (r *Reader) Detect() bool {
	for _, root := range r.Roots() {
		if st, err := os.Stat(root); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

type found struct {
	unit     source.Unit
	archived bool
}

func (r *Reader) Discover() (source.Discovery, error) {
	var d source.Discovery
	byKey := map[string]found{}
	var order []string
	add := func(full string, archived bool) {
		rel, err := filepath.Rel(r.home, full)
		if err != nil {
			return
		}
		key := unitKey(rel)
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
		u := source.Unit{Client: clients.Codex, Key: key, Path: full, Rel: rel, Size: st.Size,
			MtimeMs: scan.MsFromTimespec(st.Mtimespec), BirthMs: scan.MsFromTimespec(st.Birthtimespec)}
		prev, dup := byKey[key]
		if !dup {
			order = append(order, key)
		}
		// The same thread in both folders is one rollout mid-move: keep the longer copy, the archived one on a tie.
		if !dup || u.Size > prev.unit.Size || (u.Size == prev.unit.Size && archived && !prev.archived) {
			byKey[key] = found{u, archived}
		}
	}
	sessions := filepath.Join(r.home, sessionsDir)
	for _, year := range r.subdirs(sessions, yearDir, &d) {
		for _, month := range r.subdirs(year, dayDir, &d) {
			for _, day := range r.subdirs(month, dayDir, &d) {
				for _, name := range r.names(day, liveName, &d) {
					add(filepath.Join(day, name), false)
				}
			}
		}
	}
	archived := filepath.Join(r.home, archivedDir)
	for _, name := range r.names(archived, archiveName, &d) {
		add(filepath.Join(archived, name), true)
	}
	for _, key := range order {
		d.Units = append(d.Units, byKey[key].unit)
	}
	return d, nil
}

// unitKey names a rollout by its thread id when the file name carries one, else by its path.
func unitKey(rel string) string {
	if m := threadInName.FindStringSubmatch(filepath.Base(rel)); m != nil {
		return source.Key(clients.Codex, "thread/"+m[1])
	}
	return source.Key(clients.Codex, rel)
}

func (r *Reader) list(dir string, d *source.Discovery) []os.DirEntry {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			d.UnreadableDirs++
		}
		return nil
	}
	return entries
}

func (r *Reader) subdirs(dir string, shape *regexp.Regexp, d *source.Discovery) []string {
	var out []string
	for _, e := range r.list(dir, d) {
		if e.IsDir() && shape.MatchString(e.Name()) {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func (r *Reader) names(dir string, shape *regexp.Regexp, d *source.Discovery) []string {
	var out []string
	for _, e := range r.list(dir, d) {
		if e.Type().IsRegular() && shape.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// Identify reads the session_meta line once per new shard; a later read keeps the stored identity.
func (r *Reader) Identify(unit source.Unit, previous *source.Identity) (source.Identity, error) {
	if previous != nil {
		return *previous, nil
	}
	id := source.Identity{Project: "other", Session: "unknown", Scope: "main"}
	if m := threadInName.FindStringSubmatch(filepath.Base(unit.Path)); m != nil {
		id.Session = m[1][:8]
	}
	f, _, err := source.Open(r.Roots(), unit.Path)
	if err != nil {
		return source.Identity{}, err
	}
	defer f.Close()
	in, done, err := open(f)
	if err != nil {
		return source.Identity{}, err
	}
	defer done()
	// A stored identity is never recomputed, so a first record not readable yet is retried on the next scan.
	line, err := firstLine(in)
	if err != nil {
		return source.Identity{}, fmt.Errorf("codex: first record unreadable: %w", err)
	}
	record, err := extract.Parse(line)
	if err != nil {
		return source.Identity{}, fmt.Errorf("codex: first record unreadable: %w", err)
	}
	if extract.Get(record, "type") != "session_meta" {
		return source.Identity{}, fmt.Errorf("%w: first record is no session_meta", source.ErrUnrecognised)
	}
	payload := extract.Get(record, "payload")
	if cwd, ok := extract.Get(payload, "cwd").(string); ok && filepath.IsAbs(cwd) {
		id.Project = labels.ProjectLabel(dashed(cwd), labels.HomePrefix())
	}
	if thread, ok := extract.Get(payload, "id").(string); ok && uuidShape.MatchString(thread) {
		id.Session = thread[:8]
	}
	return id, nil
}

// dashed is Claude Code's project folder name for a path: every UTF-16 unit outside [A-Za-z0-9] becomes '-'.
func dashed(path string) string {
	var b strings.Builder
	for _, c := range path {
		switch {
		case c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'):
			b.WriteRune(c)
		case c > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func firstLine(in io.Reader) ([]byte, error) {
	var line []byte
	buf := make([]byte, 64*1024)
	for {
		n, err := in.Read(buf)
		if i := indexNewline(buf[:n]); i >= 0 {
			return append(line, buf[:i]...), nil
		}
		line = append(line, buf[:n]...)
		if len(line) > scan.MaxLineBytes {
			return nil, errors.New("first line too long")
		}
		if err == io.EOF {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func indexNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

// compressed sniffs the zstd frame magic: some .zst rollouts are plain JSONL and a .jsonl name proves nothing either.
func compressed(f *os.File) (bool, error) {
	head := make([]byte, len(zstdMagic))
	n, err := f.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return false, err
	}
	return n == len(head) && string(head) == string(zstdMagic), nil
}

// counter counts the raw bytes the decoder consumed, which become the cursor offset of a compressed unit.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func newDecoder(raw io.Reader) (*zstd.Decoder, error) {
	return zstd.NewReader(raw, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(maxWindow))
}

func open(f *os.File) (io.Reader, func(), error) {
	z, err := compressed(f)
	if err != nil {
		return nil, nil, err
	}
	raw := io.NewSectionReader(f, 0, math.MaxInt64)
	if !z {
		return raw, func() {}, nil
	}
	dec, err := newDecoder(raw)
	if err != nil {
		return nil, nil, err
	}
	return dec, dec.Close, nil
}

// pass is one read's state; seen holds the event IDs already in the cursor, so a repeat anywhere counts once.
type pass struct {
	cur        source.Cursor
	unknown    bool
	tickErr    error
	seen       map[string]bool
	fallbackTs int64
	maxItems   int
}

func newPass(from source.Cursor, unit source.Unit, maxItems int) *pass {
	p := &pass{cur: from, seen: map[string]bool{}, fallbackTs: int64(math.Floor(unit.MtimeMs / 1000)),
		maxItems: maxItems}
	pending := make(map[string]string, len(from.Pending))
	for k, v := range from.Pending {
		pending[k] = v
	}
	p.cur.Pending = pending
	p.cur.Events = append([]extract.Event(nil), from.Events...)
	for _, e := range p.cur.Events {
		p.seen[e.ID] = true
	}
	return p
}

func (p *pass) full() bool {
	return p.maxItems > 0 && len(p.cur.Events)+len(p.cur.Pending) >= p.maxItems
}

func clampTs(sec int64) int64 { return min(extract.MaxTs, max(extract.MinTs, sec)) }

func count(v any) int64 {
	f, ok := v.(float64)
	if !ok || math.IsInf(f, 0) || f != f || !(f > 0 && f <= maxTokens) {
		return 0
	}
	return int64(extract.RoundJS(f))
}

var usageFields = [5]string{"input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens",
	"total_tokens"}

// usage is ok only when input_tokens or output_tokens is a number: a renamed or retyped field is no zero-token call.
func usage(v any) ([5]int64, bool) {
	var u [5]int64
	if _, ok := v.(map[string]any); !ok {
		return u, false
	}
	if !source.Finite(extract.Get(v, "input_tokens")) && !source.Finite(extract.Get(v, "output_tokens")) {
		return u, false
	}
	for i, k := range usageFields {
		u[i] = count(extract.Get(v, k))
	}
	return u, true
}

func joined(u [5]int64) string {
	parts := make([]string, len(u))
	for i, n := range u {
		parts[i] = strconv.FormatInt(n, 10)
	}
	return strings.Join(parts, ",")
}

// line applies one complete record. A token_count's ID is its cumulative totals, which a fork or resume copies
// unchanged and which differ between requests of one lineage.
func (p *pass) line(data []byte) {
	if len(data) == 0 {
		return
	}
	p.cur.Lines++
	record, err := extract.Parse(data)
	if _, ok := record.(map[string]any); err != nil || !ok {
		p.cur.Skipped++
		p.unknown = true
		return
	}
	ts := p.cur.LastTs
	stamp := extract.Get(record, "timestamp")
	if ms, finite, err := extract.DateParse(stamp); err == nil && finite {
		ts = clampTs(int64(math.Floor(ms / 1000)))
		p.cur.LastTs = ts
	} else if ts == 0 {
		ts = p.fallbackTs
	}
	ts = clampTs(ts)
	payload := extract.Get(record, "payload")
	kind := extract.Get(record, "type")
	switch kind {
	case "session_meta":
	case "turn_context", "event_msg", "response_item", "compacted":
		p.cur.MarkKnown()
	default:
		p.unknown = true
	}
	switch kind {
	case "turn_context":
		p.cur.Pending[pendingModel] = modelLabel(extract.Get(payload, "model"))
	case "event_msg":
		if extract.Get(payload, "type") != "token_count" {
			return
		}
		info := extract.Get(payload, "info")
		if info == nil {
			return
		}
		total, ok1 := usage(extract.Get(info, "total_token_usage"))
		last, ok2 := usage(extract.Get(info, "last_token_usage"))
		if !ok1 || !ok2 {
			p.cur.Skipped++
			// A malformed usage may be format drift, which an earlier withheld record must not hide.
			p.cur.Pending[source.PendingWithheld] = "0"
			return
		}
		key := joined(total)
		if p.cur.Pending[pendingTotals] == key {
			return
		}
		id := extract.HashID("codex:token_count:" + key)
		if p.seen[id] {
			p.cur.Pending[pendingTotals] = key
			return
		}
		if p.full() {
			p.cur.Skipped++
			return
		}
		p.cur.Pending[pendingTotals] = key
		// Imported threads record only total_tokens, which cannot be split into input, cache and output.
		if last[0] == 0 && last[2] == 0 && last[4] > 0 {
			p.cur.Skipped++
			p.cur.MarkWithheld()
			return
		}
		p.seen[id] = true
		if source.BadTs(stamp) {
			p.cur.Skipped++
		}
		cached := min(last[1], last[0])
		model := p.cur.Pending[pendingModel]
		if model == "" {
			model = "unknown"
		}
		p.cur.Events = append(p.cur.Events, extract.Event{Kind: extract.Call, Ts: ts, ID: id, Text: model,
			N: [5]int64{last[0] - cached, 0, 0, cached, last[2]}})
	}
}

// modelLabel is the label the store keeps, so a name this reader accepts is never turned into unknown later.
func modelLabel(v any) string { return labels.Model(clients.Codex, v) }

// lines feeds every complete line of in to p and returns the bytes after the last newline, nil when that tail
// passed scan.MaxLineBytes; consumed counts the bytes up to and including the last newline.
func (p *pass) lines(in io.Reader, tick func() error) (tail []byte, consumed int64, err error) {
	var held []byte
	var lineBytes int64
	over := false
	chunk := make([]byte, readChunk)
	for {
		got, rerr := in.Read(chunk)
		if got > 0 && tick != nil {
			if p.tickErr = tick(); p.tickErr != nil {
				return nil, consumed, p.tickErr
			}
		}
		data := chunk[:got]
		at0 := 0
		for {
			at := indexNewline(data[at0:])
			if at < 0 {
				break
			}
			piece := data[at0 : at0+at]
			lineBytes += int64(len(piece))
			switch {
			case over || lineBytes > scan.MaxLineBytes:
				p.cur.Lines++
				p.cur.Skipped++
				p.unknown = true
			case len(held) > 0:
				p.line(append(held, piece...))
			default:
				p.line(piece)
			}
			consumed += lineBytes + 1
			held, lineBytes, over = held[:0], 0, false
			at0 += at + 1
		}
		if at0 < got {
			lineBytes += int64(got - at0)
			if lineBytes > scan.MaxLineBytes {
				held, over = nil, true
			} else {
				held = append(held, data[at0:]...)
			}
		}
		if rerr == io.EOF {
			if over {
				return nil, consumed, nil
			}
			return held, consumed, nil
		}
		if rerr != nil {
			return held, consumed, rerr
		}
	}
}

// Read resumes a plain rollout at from.Offset like a Claude transcript. A compressed one is never appended to, so it
// is decoded whole every time and its offset is its compressed size.
func (r *Reader) Read(f *os.File, unit source.Unit, from source.Cursor, opt source.Options) (source.Cursor, error) {
	z, err := compressed(f)
	if err != nil {
		return source.Cursor{}, err
	}
	if z {
		return r.readCompressed(f, unit, from, opt)
	}
	p := newPass(from, unit, opt.MaxItems)
	tail, consumed, err := p.lines(io.NewSectionReader(f, from.Offset, math.MaxInt64-from.Offset), opt.Tick)
	if err != nil {
		return source.Cursor{}, err
	}
	p.cur.Offset = from.Offset + consumed
	// A last line without its newline is taken only when it parses whole; otherwise it is still being written.
	if len(tail) > 0 {
		if _, err := extract.Parse(tail); err == nil {
			p.line(tail)
			p.cur.Offset += int64(len(tail))
		}
	}
	if err := source.Unrecognised(unit, from, &p.cur, p.unknown); err != nil {
		return source.Cursor{}, err
	}
	return p.cur, nil
}

// maxDecoded bounds what one compressed rollout may expand to; a var so a test can lower it. The largest rollout seen
// (2026-11-16) was 2.9 MB compressed; one over it fails as unreadable, so a cut is never stored as complete.
var maxDecoded int64 = 256 << 20

// ErrTooLarge marks a compressed rollout that decodes past maxDecoded.
var ErrTooLarge error = codedError("EDECODECAP")

type codedError string

func (e codedError) Error() string {
	return "codex: compressed rollout decodes past its cap (" + string(e) + ")"
}
func (e codedError) Code() string { return string(e) }

// capped ends the stream at left bytes and records that it cut it.
type capped struct {
	r    io.Reader
	left int64
	cut  bool
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		// A stream of exactly the cap ends here; only a byte past it is a cut.
		var one [1]byte
		for {
			n, err := c.r.Read(one[:])
			if n > 0 {
				c.cut = true
				return 0, io.EOF
			}
			if err != nil {
				return 0, err
			}
		}
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (r *Reader) readCompressed(f *os.File, unit source.Unit, from source.Cursor, opt source.Options) (
	source.Cursor, error) {
	raw := &counter{r: io.NewSectionReader(f, 0, math.MaxInt64)}
	dec, err := newDecoder(raw)
	if err != nil {
		return source.Cursor{}, err
	}
	defer dec.Close()
	p := newPass(source.Cursor{}, unit, opt.MaxItems)
	out := &capped{r: dec, left: maxDecoded}
	tail, _, err := p.lines(out, opt.Tick)
	if p.tickErr != nil {
		return source.Cursor{}, p.tickErr
	}
	// A corrupt or cut stream fails the read, so the previous shard stays.
	if err != nil {
		return source.Cursor{}, fmt.Errorf("codex: compressed rollout: %w", err)
	}
	if out.cut {
		return source.Cursor{}, ErrTooLarge
	}
	if len(tail) > 0 {
		if _, perr := extract.Parse(tail); perr == nil {
			p.line(tail)
		} else {
			p.cur.Lines++
			p.cur.Skipped++
		}
	}
	if len(p.cur.Events) < len(from.Events) {
		return source.Cursor{}, fmt.Errorf("%w: %d events, cursor holds %d", ErrNoResume, len(p.cur.Events),
			len(from.Events))
	}
	p.cur.Offset = raw.n
	if err := source.Unrecognised(unit, source.Cursor{}, &p.cur, p.unknown); err != nil {
		return source.Cursor{}, err
	}
	return p.cur, nil
}
