// Package guardlog reads the per-call guard log (contract v1, CM-00021) read-only and keeps only its counts.
package guardlog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// MaxLine is the contract's line limit, without the newline.
	MaxLine = 512
	// MaxPass bounds the bytes one pass reads over all files; the rest waits for the next pass.
	MaxPass  = 32 << 20
	MaxMs    = 600000
	Rotated  = 5
	fileName = "guard.log"
	// maxTracked bounds the stored positions: the files of one pass plus as many unseen ones.
	maxTracked = 2 * (Rotated + 1)
)

var RulePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)

// ErrNotRegular is the collector's code when guard.log itself is a link or not a regular file.
var ErrNotRegular = errors.New("ENOTREGULAR")

// Dir is where claude-guard writes the log under home.
func Dir(home string) string { return filepath.Join(home, ".claude", "hooks", "state") }

// File is the position kept for one log file; First is the 16-hex hash of its first line, never the line.
type File struct {
	Dev    uint64 `json:"dev"`
	Ino    uint64 `json:"ino"`
	First  string `json:"first"`
	Offset int64  `json:"off"`
	// Skip means Offset is inside an oversized line whose end has not been read yet.
	Skip bool `json:"skip,omitempty"`
}

type Skipped struct {
	Malformed, Oversized, BadVersion, BadRule, NotRegular, Truncated int64
}

type HourKey struct {
	Day      time.Time
	Hour     int
	Tool     string
	Decision string
	Sub      bool
}

type HourCount struct {
	Calls, TimeCaps, FailClosed, SubDeny int64
}

type LatencyKey struct {
	Day  time.Time
	Tool string
	Ms   int64
}

type RuleKey struct {
	Day            time.Time
	Tool, Decision string
	Rule           string
}

type Deny struct {
	At   time.Time
	Tool string
	Rule string
}

// Pass is what one read found: the counts to add and the positions to keep once they are stored.
type Pass struct {
	Files   []File
	Hours   map[HourKey]*HourCount
	Latency map[LatencyKey]int64
	Rules   map[RuleKey]int64
	Skipped Skipped
	Deny    *Deny
	// Present is false when guard.log and every rotation are absent.
	Present bool
	// Refused is true when any log file of the pass was refused as a link or non-regular file.
	Refused bool
	Lines   int64
}

type Reader struct {
	Dir string
	// Loc gives the local day and hour; nil means time.Local.
	Loc *time.Location
	// Budget overrides MaxPass in tests.
	Budget int64
}

func (r Reader) loc() *time.Location {
	if r.Loc != nil {
		return r.Loc
	}
	return time.Local
}

// Read reads every complete line past the tracked positions, oldest file first. It never changes tracked.
func (r Reader) Read(tracked []File) (Pass, error) {
	p := Pass{Hours: map[HourKey]*HourCount{}, Latency: map[LatencyKey]int64{}, Rules: map[RuleKey]int64{}}
	budget := r.Budget
	if budget <= 0 {
		budget = MaxPass
	}
	byID := map[[2]uint64]File{}
	for _, f := range tracked {
		byID[[2]uint64{f.Dev, f.Ino}] = f
	}
	seen := map[[2]uint64]bool{}
	for i := Rotated; i >= 0; i-- {
		name := fileName
		if i > 0 {
			name += "." + strconv.Itoa(i)
		}
		refusedBefore := p.Skipped.NotRegular
		f, present, err := r.readFile(filepath.Join(r.Dir, name), byID, seen, &budget, &p)
		if err != nil {
			return Pass{}, err
		}
		if !present {
			continue
		}
		p.Present = true
		if p.Skipped.NotRegular > refusedBefore {
			p.Refused = true
		}
		if f == nil {
			continue
		}
		p.Files = append(p.Files, *f)
	}
	// A rename during the pass can hide a tracked file from every name; keeping its position stops a re-read later.
	for _, f := range tracked {
		if len(p.Files) >= maxTracked {
			break
		}
		if !seen[[2]uint64{f.Dev, f.Ino}] {
			p.Files = append(p.Files, f)
		}
	}
	return p, nil
}

// readFile returns the file's new position, nil when it was refused, and present=false when it does not exist.
func (r Reader) readFile(path string, byID map[[2]uint64]File, seen map[[2]uint64]bool, budget *int64,
	p *Pass) (*File, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		p.Skipped.NotRegular++
		return nil, true, nil
	}
	lst, _ := info.Sys().(*syscall.Stat_t)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil, false, nil
	case errors.Is(err, syscall.ELOOP):
		p.Skipped.NotRegular++
		return nil, true, nil
	case err != nil:
		return nil, false, &os.PathError{Op: "open", Path: path, Err: err}
	}
	fh := os.NewFile(uintptr(fd), path)
	defer fh.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, false, err
	}
	if lst == nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || uint64(st.Dev) != uint64(lst.Dev) ||
		st.Ino != lst.Ino {
		p.Skipped.NotRegular++
		return nil, true, nil
	}
	id := [2]uint64{uint64(st.Dev), st.Ino}
	if seen[id] {
		return nil, true, nil
	}
	seen[id] = true
	first, err := firstLine(fh)
	if err != nil {
		return nil, false, err
	}
	pos := File{Dev: id[0], Ino: id[1], First: first}
	if old, ok := byID[id]; ok && old.First == first && old.Offset > 0 {
		pos.Offset, pos.Skip = old.Offset, old.Skip
		if st.Size < old.Offset {
			p.Skipped.Truncated++
			pos.Offset, pos.Skip = 0, false
		}
	}
	n := st.Size - pos.Offset
	if n > *budget {
		n = *budget
	}
	if n <= 0 {
		return &pos, true, nil
	}
	buf := make([]byte, n)
	got, err := fh.ReadAt(buf, pos.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	buf = buf[:got]
	start := 0
	if pos.Skip {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			*budget -= int64(got)
			pos.Offset += int64(got)
			return &pos, true, nil
		}
		start, pos.Skip = i+1, false
	}
	end := start + bytes.LastIndexByte(buf[start:], '\n') + 1
	consumed := end
	// A tail past MaxLine+1 bytes is an oversized line whatever follows, so it is counted now and its end skipped.
	if len(buf)-end > MaxLine+1 {
		p.Lines++
		p.Skipped.Oversized++
		pos.Skip = true
		consumed = len(buf)
	}
	*budget -= int64(consumed)
	for _, line := range bytes.SplitAfter(buf[start:end], []byte{'\n'}) {
		if len(line) > 0 {
			r.count(line[:len(line)-1], p)
		}
	}
	pos.Offset += int64(consumed)
	return &pos, true, nil
}

func firstLine(fh *os.File) (string, error) {
	buf := make([]byte, MaxLine+1)
	n, err := fh.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	buf = buf[:n]
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[:i+1]
	} else if n <= MaxLine {
		return "", nil
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:8]), nil
}

type line struct {
	V     json.RawMessage `json:"v"`
	Ts    *string         `json:"ts"`
	Tool  *string         `json:"tool"`
	Eng   *string         `json:"eng"`
	D     *string         `json:"d"`
	Ms    json.RawMessage `json:"ms"`
	Sub   *bool           `json:"sub"`
	Flags *[]string       `json:"flags"`
	Rule  *string         `json:"rule"`
}

var integer = regexp.MustCompile(`^(?:0|[1-9][0-9]{0,6})$`)

func (r Reader) count(b []byte, p *Pass) {
	p.Lines++
	if len(b) > MaxLine {
		p.Skipped.Oversized++
		return
	}
	var l line
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(b, &l) != nil {
		p.Skipped.Malformed++
		return
	}
	if l.V == nil {
		p.Skipped.Malformed++
		return
	}
	if string(l.V) != "1" {
		p.Skipped.BadVersion++
		return
	}
	if l.Ts == nil || l.Tool == nil || l.Eng == nil || l.D == nil || l.Sub == nil || !integer.Match(l.Ms) {
		p.Skipped.Malformed++
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, *l.Ts)
	ms, _ := strconv.ParseInt(string(l.Ms), 10, 64)
	if err != nil || !strings.HasSuffix(*l.Ts, "Z") || ms > MaxMs || !oneOf(*l.Tool, "bash", "file") ||
		!oneOf(*l.Eng, "go", "node") || !oneOf(*l.D, "allow", "ask", "deny") {
		p.Skipped.Malformed++
		return
	}
	var c HourCount
	c.Calls = 1
	if l.Flags != nil {
		for _, f := range *l.Flags {
			switch f {
			case "time_cap":
				c.TimeCaps = 1
			case "fail_closed":
				c.FailClosed = 1
			case "sub_deny":
				c.SubDeny = 1
			default:
				p.Skipped.Malformed++
				return
			}
		}
	}
	rule := ""
	if l.Rule != nil {
		if RulePattern.MatchString(*l.Rule) && *l.D != "allow" {
			rule = *l.Rule
		} else {
			p.Skipped.BadRule++
		}
	}
	local := ts.In(r.loc())
	y, m, d := local.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	k := HourKey{Day: day, Hour: local.Hour(), Tool: *l.Tool, Decision: *l.D, Sub: *l.Sub}
	h := p.Hours[k]
	if h == nil {
		h = &HourCount{}
		p.Hours[k] = h
	}
	h.Calls += c.Calls
	h.TimeCaps += c.TimeCaps
	h.FailClosed += c.FailClosed
	h.SubDeny += c.SubDeny
	p.Latency[LatencyKey{Day: day, Tool: *l.Tool, Ms: ms}]++
	if rule != "" {
		p.Rules[RuleKey{Day: day, Tool: *l.Tool, Decision: *l.D, Rule: rule}]++
	}
	if *l.D == "deny" && (p.Deny == nil || ts.After(p.Deny.At)) {
		p.Deny = &Deny{At: ts.UTC(), Tool: *l.Tool, Rule: rule}
	}
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
