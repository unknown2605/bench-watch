package transcripts

import (
	"os"
	"sync"

	"benchwatch/server/internal/clients"
	clientsource "benchwatch/server/internal/source"
)

// ClientCounts is one client's share of a rebuild's skipped lines and unreadable units.
type ClientCounts struct {
	Client              clients.ID
	Skipped, Unreadable int64
}

// unreadable counts, per client, the units one run could not list or read. The store keeps only the total, which
// also counts files it failed to open or to save; those have no client here.
type unreadable struct {
	mu sync.Mutex
	n  map[clients.ID]int64
}

func (u *unreadable) add(c clients.ID, n int) {
	if n == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.n == nil {
		u.n = map[clients.ID]int64{}
	}
	u.n[c] += int64(n)
}

func (u *unreadable) reset() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.n = nil
}

func (u *unreadable) get(c clients.ID) int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.n[c]
}

// counting passes every call to its reader and files the failures under the reader's client.
type counting struct {
	clientsource.Reader
	counts *unreadable
}

func (c counting) Discover() (clientsource.Discovery, error) {
	d, err := c.Reader.Discover()
	if err == nil {
		c.counts.add(c.Client(), d.UnreadableDirs+len(d.UnreadableKeys))
	}
	return d, err
}

func (c counting) Identify(unit clientsource.Unit, previous *clientsource.Identity) (clientsource.Identity, error) {
	id, err := c.Reader.Identify(unit, previous)
	if err != nil {
		c.counts.add(c.Client(), 1)
	}
	return id, err
}

func (c counting) Read(f *os.File, unit clientsource.Unit, from clientsource.Cursor, opt clientsource.Options) (
	clientsource.Cursor, error) {
	cur, err := c.Reader.Read(f, unit, from, opt)
	if err != nil {
		c.counts.add(c.Client(), 1)
	}
	return cur, err
}

// byClient lists every client found on this machine with its share of the run's counts.
func byClient(found []clients.ID, skipped map[clients.ID]int64, counts *unreadable) []ClientCounts {
	out := make([]ClientCounts, 0, len(found))
	for _, c := range found {
		out = append(out, ClientCounts{Client: c, Skipped: skipped[c], Unreadable: counts.get(c)})
	}
	return out
}
