// Package source is the boundary between the scanner and each client's usage records: one Reader per client turns
// its records into extract events, and every file a reader opens passes the guard in guard.go first.
package source

import (
	"os"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
)

type Client = clients.ID

// Unit is one record store of a client, a transcript file today. Path and Rel never leave the scanner.
type Unit struct {
	Client  Client
	Key     string
	Path    string
	Rel     string
	Size    int64
	MtimeMs float64
	BirthMs float64
}

// Identity is what a unit's events are filed under; every field is already a label.
type Identity struct {
	Project   string
	Session   string
	Scope     string
	AgentType *string
}

func (a Identity) Equal(b Identity) bool {
	sameAgent := (a.AgentType == nil) == (b.AgentType == nil) && (a.AgentType == nil || *a.AgentType == *b.AgentType)
	return a.Project == b.Project && a.Session == b.Session && a.Scope == b.Scope && sameAgent
}

// Cursor is where the last read of a unit stopped. Events is the whole list, because a later record that repeats a
// call ID raises the counts of the earlier event.
type Cursor struct {
	Offset  int64
	LastTs  int64
	Lines   int64
	Skipped int64
	Pending map[string]string
	Events  []extract.Event
}

type Options struct {
	// Tick runs once per chunk read and stops the read with its error.
	Tick func() error
	// MaxItems bounds Events plus Pending.
	MaxItems int
	// Repeat says whether this unit already returned the same content for the same result label; nil marks none.
	Repeat func(label string, content any) (bool, error)
}

type Discovery struct {
	Units []Unit
	// UnreadableKeys exist but could not be stat'ed; their last shard is kept.
	UnreadableKeys []string
	// While any folder is unreadable no unit of this client counts as deleted.
	UnreadableDirs int
}

// Reader reads one client's records. Event IDs are extract.HashID of a record ID that survives a copy into another
// unit (fork, resume), so the scanner's dedupe keeps one copy and the earliest unit owns it.
type Reader interface {
	Client() Client
	// Roots are the absolute folders this reader may open files under; Open refuses anything else.
	Roots() []string
	// Detect reports whether the client has records on this machine.
	Detect() bool
	Discover() (Discovery, error)
	// Identify gives a unit's identity; previous is its stored shard's identity, nil for a new shard.
	Identify(unit Unit, previous *Identity) (Identity, error)
	// Read continues the unit at from.Offset in f, opened through Open, and stops after the last complete record.
	Read(f *os.File, unit Unit, from Cursor, opt Options) (Cursor, error)
}

// Key is a unit's shard key. The client prefix keeps one client's ownership order as it was before keys had one.
func Key(client Client, rel string) string {
	return string(client) + "-" + extract.HashKey([]byte(rel))
}
