package collect

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"
)

type State string

const (
	StateOK          State = "ok"
	StatePartial     State = "partial"
	StateUnavailable State = "unavailable"
	StatePaused      State = "paused"
)

const (
	SourceMachine      = "machine"
	SourceDocker       = "docker"
	SourceDockerEvents = "dockerEvents"
	SourceHistory      = "history"
)

// Part names a piece of a sample that can fail on its own; samples and status list them in Missing.
const (
	PartLoad    = "load"
	PartMemory  = "memory"
	PartSwap    = "swap"
	PartVolumes = "volumes"
	PartDiskIO  = "diskIO"
	PartNet     = "net"
	PartApps    = "apps"
	PartStats   = "stats"
	PartRates   = "rates"
	PartInfo    = "info"
	PartDisk    = "disk"
)

type PartError struct {
	Part string
	Code string
}

func NewPartError(part string, err error) PartError {
	return PartError{Part: part, Code: ErrorCode(err)}
}

// Drops counts history rows given up: refused by the sink as permanent, past the count cap, or past the age cap.
type Drops struct {
	Rejected uint64 `json:"rejected"`
	Overflow uint64 `json:"overflow"`
	Expired  uint64 `json:"expired"`
}

func (d Drops) Total() uint64 { return d.Rejected + d.Overflow + d.Expired }

// SourceStatus carries error codes from a fixed set only, never error text, which can hold paths or daemon messages.
type SourceStatus struct {
	State     State     `json:"state"`
	LastError string    `json:"lastError,omitempty"`
	Missing   []string  `json:"missing,omitempty"`
	Since     time.Time `json:"since"`
	LastOK    time.Time `json:"lastOK,omitzero"`
	Drops     Drops     `json:"drops,omitzero"`
}

type Status struct {
	mu  sync.RWMutex
	now func() time.Time
	m   map[string]SourceStatus
}

func NewStatus(now func() time.Time) *Status {
	if now == nil {
		now = RealClock().Now
	}
	return &Status{now: now, m: map[string]SourceStatus{}}
}

func (s *Status) set(source string, state State, code string, missing []string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.now()
	cur := s.m[source]
	if cur.State != state {
		cur.Since = t
	}
	cur.State, cur.Missing = state, missing
	if code != "" {
		cur.LastError = code
	}
	if ok {
		cur.LastOK = t
	}
	s.m[source] = cur
}

// OK keeps the last error code visible after recovery, until the next failure replaces it.
func (s *Status) OK(source string) { s.set(source, StateOK, "", nil, true) }

func (s *Status) Fail(source string, state State, err error) {
	s.set(source, state, ErrorCode(err), nil, false)
}

// Report: the first failed part's code becomes LastError.
func (s *Status) Report(source string, parts []PartError) {
	if len(parts) == 0 {
		s.OK(source)
		return
	}
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		if !slices.Contains(names, p.Part) {
			names = append(names, p.Part)
		}
	}
	s.set(source, StatePartial, parts[0].Code, names, true)
}

func (s *Status) AddDrops(source string, d Drops) {
	if d.Total() == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.m[source]
	cur.Drops.Rejected += d.Rejected
	cur.Drops.Overflow += d.Overflow
	cur.Drops.Expired += d.Expired
	s.m[source] = cur
}

func (s *Status) Get(source string) SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[source]
}

func (s *Status) Snapshot() map[string]SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]SourceStatus, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out
}

var Codes = []string{"ENOENT", "ECONNREFUSED", "ECONNRESET", "EPIPE", "EACCES", "EPERM", "ETIMEDOUT", "ESYSCALL",
	"ECANCELED", "EEOF", "EDECODE", "EGUARD", "ENOTFOUND", "ENOSINK", "EPERMANENT", "ETOOLARGE", "EOTHER"}

// CodedError lets a package name its own code; one outside Codes reads as EOTHER.
type CodedError interface{ Code() string }

func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var ce CodedError
	if errors.As(err, &ce) {
		if c := ce.Code(); slices.Contains(Codes, c) {
			return c
		}
		return "EOTHER"
	}
	if IsPermanent(err) {
		return "EPERMANENT"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ENOENT:
			return "ENOENT"
		case syscall.ECONNREFUSED:
			return "ECONNREFUSED"
		case syscall.ECONNRESET:
			return "ECONNRESET"
		case syscall.EPIPE:
			return "EPIPE"
		case syscall.EACCES:
			return "EACCES"
		case syscall.EPERM:
			return "EPERM"
		case syscall.ETIMEDOUT:
			return "ETIMEDOUT"
		}
		return "ESYSCALL"
	}
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "ETIMEDOUT"
	case errors.As(err, &ne) && ne.Timeout():
		return "ETIMEDOUT"
	case errors.Is(err, context.Canceled):
		return "ECANCELED"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "EEOF"
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.As(err, &se) || errors.As(err, &te) {
		return "EDECODE"
	}
	return "EOTHER"
}
