package scan

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
	"benchwatch/server/internal/source"
)

// ClaudeReader reads Claude Code's transcripts under one projects folder.
type ClaudeReader struct {
	root string
	dirs map[string]listing
}

var _ source.Reader = (*ClaudeReader)(nil)

// NewClaudeReader takes the projects folder; the guard compares cleaned absolute paths, so root is made one here.
func NewClaudeReader(root string) *ClaudeReader {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &ClaudeReader{root: root}
}

func (r *ClaudeReader) Client() source.Client { return clients.ClaudeCode }

func (r *ClaudeReader) Roots() []string { return []string{r.root} }

func (r *ClaudeReader) Detect() bool {
	st, err := os.Stat(r.root)
	return err == nil && st.IsDir()
}

func (r *ClaudeReader) Discover() (source.Discovery, error) {
	files, unreadableStats, unreadableDirs, walked := listTranscripts(r.root, r.dirs)
	r.dirs = walked
	d := source.Discovery{UnreadableKeys: unreadableStats, UnreadableDirs: unreadableDirs}
	for _, file := range files {
		d.Units = append(d.Units, source.Unit{Client: clients.ClaudeCode, Key: file.key, Path: file.full,
			Rel: file.rel, Size: file.size, MtimeMs: file.mtimeMs, BirthMs: file.birthMs})
	}
	return d, nil
}

// Identify reads a subagent's type again while it is unknown: Claude Code writes the meta file after the transcript.
func (r *ClaudeReader) Identify(unit source.Unit, previous *source.Identity) (source.Identity, error) {
	var id source.Identity
	if previous != nil {
		id = *previous
	} else {
		id.Project, id.Session, id.Scope = identity(unit.Rel)
	}
	if id.Scope == "sub" && (id.AgentType == nil || *id.AgentType == "unknown") {
		agent, err := r.agentType(unit.Path)
		if err != nil {
			return source.Identity{}, err
		}
		id.AgentType = &agent
	}
	return id, nil
}

// agentType fails only when the guard refuses the meta file; a missing or unreadable one is a subagent of unknown type.
func (r *ClaudeReader) agentType(full string) (string, error) {
	f, size, err := source.Open(r.Roots(), strings.TrimSuffix(full, ".jsonl")+".meta.json")
	if errors.Is(err, source.ErrOutsideRoots) || errors.Is(err, source.ErrForbiddenName) ||
		errors.Is(err, source.ErrRelativePath) {
		return "", err
	}
	if err != nil {
		return "unknown", nil
	}
	defer f.Close()
	if size > metaMaxBytes {
		return "unknown", nil
	}
	data, err := io.ReadAll(io.LimitReader(f, metaMaxBytes))
	if err != nil {
		return "unknown", nil
	}
	parsed, err := extract.Parse(data)
	if err != nil || parsed == nil {
		return "unknown", nil
	}
	return labels.AgentLabel(extract.Get(parsed, "agentType")), nil
}

// Read takes lines up to the last newline, plus a final unterminated line only when it parses whole; the cursor
// holds no more than opt.MaxItems events and pending results.
func (r *ClaudeReader) Read(f *os.File, unit source.Unit, from source.Cursor, opt source.Options) (source.Cursor, error) {
	cur := from
	state := &extract.State{Pending: cur.Pending, Calls: map[string]int{}, LastTs: cur.LastTs, Events: cur.Events,
		MaxItems: opt.MaxItems}
	var full error
	state.Repeat = func(label string, content any) bool {
		if opt.Repeat == nil {
			return false
		}
		repeat, err := opt.Repeat(label, content)
		if err != nil {
			full = err
		}
		return repeat
	}
	for i, e := range cur.Events {
		if e.Kind == extract.Call {
			state.Calls[e.ID] = i
		}
	}
	fallbackTs := int64(math.Floor(unit.MtimeMs / 1000))
	count := func() bool {
		cur.Lines++
		if len(state.Events)+len(state.Pending) >= opt.MaxItems {
			cur.Skipped++
			return false
		}
		return true
	}
	apply := func(record any, err error) {
		if err == nil {
			err = extract.ApplyRecord(record, state, fallbackTs)
		}
		if err != nil {
			cur.Skipped++
		}
	}
	take := func(line []byte) {
		if len(line) > 0 && count() {
			apply(extract.Parse(line))
		}
	}
	offset, readAt := cur.Offset, cur.Offset
	var held []byte
	var lineBytes int64
	chunk := make([]byte, readChunk)
	for {
		got, err := f.ReadAt(chunk, readAt)
		if err != nil && err != io.EOF {
			return source.Cursor{}, err
		}
		if got == 0 {
			break
		}
		if opt.Tick != nil {
			if err := opt.Tick(); err != nil {
				return source.Cursor{}, err
			}
		}
		readAt += int64(got)
		data := chunk[:got]
		at0 := 0
		for {
			at := bytes.IndexByte(data[at0:], '\n')
			if at < 0 {
				break
			}
			piece := data[at0 : at0+at]
			lineBytes += int64(len(piece))
			if lineBytes > MaxLineBytes {
				cur.Lines++
				cur.Skipped++
			} else if len(held) > 0 {
				held = append(held, piece...)
				take(held)
			} else {
				take(piece)
			}
			offset += lineBytes + 1
			held, lineBytes = held[:0], 0
			if cap(held) > readChunk {
				held = nil
			}
			at0 += at + 1
		}
		if full != nil {
			return source.Cursor{}, full
		}
		if at0 < got {
			lineBytes += int64(got - at0)
			if lineBytes > MaxLineBytes {
				held = nil
			} else {
				held = append(held, data[at0:]...)
			}
		}
	}
	if len(held) > 0 {
		if record, err := extract.Parse(held); err == nil {
			if count() {
				apply(record, nil)
			}
			offset += int64(len(held))
		}
	}
	if full != nil {
		return source.Cursor{}, full
	}
	cur.Offset, cur.LastTs, cur.Pending, cur.Events = offset, state.LastTs, state.Pending, state.Events
	return cur, nil
}
