package transcripts

import (
	"errors"
	"os"
	"testing"

	"benchwatch/server/internal/clients"
	clientsource "benchwatch/server/internal/source"
)

type failing struct{ clientsource.Reader }

func (failing) Client() clients.ID { return clients.Codex }

func (failing) Discover() (clientsource.Discovery, error) {
	return clientsource.Discovery{UnreadableDirs: 1, UnreadableKeys: []string{"codex-a"}}, nil
}

func (failing) Identify(clientsource.Unit, *clientsource.Identity) (clientsource.Identity, error) {
	return clientsource.Identity{}, errors.New("first record unreadable")
}

func (failing) Read(*os.File, clientsource.Unit, clientsource.Cursor, clientsource.Options) (clientsource.Cursor,
	error) {
	return clientsource.Cursor{}, clientsource.ErrUnrecognised
}

func TestUnreadableUnitsAreCountedUnderTheirClient(t *testing.T) {
	counts := &unreadable{}
	r := counting{Reader: failing{}, counts: counts}
	_, _ = r.Discover()
	_, _ = r.Identify(clientsource.Unit{}, nil)
	_, _ = r.Read(nil, clientsource.Unit{}, clientsource.Cursor{}, clientsource.Options{})
	got := byClient([]clients.ID{clients.ClaudeCode, clients.Codex},
		map[clients.ID]int64{clients.Codex: 7, clients.OpenCode: 2}, counts)
	want := []ClientCounts{{Client: clients.ClaudeCode}, {Client: clients.Codex, Skipped: 7, Unreadable: 4}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("by client %+v, want %+v", got, want)
	}
	counts.reset()
	if counts.get(clients.Codex) != 0 {
		t.Error("a new run kept the last run's count")
	}
}
