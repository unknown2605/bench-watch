// Package codelog writes components' error codes to the log, never error text, which can hold paths or values.
package codelog

import (
	"log"
	"regexp"
	"sync"
	"time"
)

var code = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

// Clean is the code itself when it has the API's ErrorCode shape, "ok" for none and EOTHER for anything else.
func Clean(c string) string {
	switch {
	case c == "":
		return "ok"
	case code.MatchString(c):
		return c
	}
	return "EOTHER"
}

// Changes logs "<what>: <code>" when a component's code changes, and "<what>: ok" when it clears; what is a fixed
// label chosen by the caller.
type Changes struct {
	mu   sync.Mutex
	last map[string]string
}

var Default = &Changes{}

func (c *Changes) Note(what, errCode string) {
	errCode = Clean(errCode)
	c.mu.Lock()
	if c.last == nil {
		c.last = map[string]string{}
	}
	prev, seen := c.last[what]
	c.last[what] = errCode
	c.mu.Unlock()
	if seen && prev == errCode || !seen && errCode == "ok" {
		return
	}
	log.Printf("%s: %s", what, errCode)
}

// Note records a change on Default.
func Note(what, errCode string) { Default.Note(what, errCode) }

// Limiter logs one line per label and code at most once per Every, for failures that repeat per request.
type Limiter struct {
	Every time.Duration
	Now   func() time.Time

	mu sync.Mutex
	at map[string]time.Time
}

func (l *Limiter) Print(what, errCode string) {
	errCode = Clean(errCode)
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	key := what + "\x00" + errCode
	l.mu.Lock()
	if l.at == nil {
		l.at = map[string]time.Time{}
	}
	last, seen := l.at[key]
	due := !seen || now.Sub(last) >= l.Every
	if due {
		l.at[key] = now
	}
	l.mu.Unlock()
	if due {
		log.Printf("%s: %s", what, errCode)
	}
}
