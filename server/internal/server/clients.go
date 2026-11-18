package server

import (
	"errors"

	"benchwatch/server/internal/api"
	"benchwatch/server/internal/clients"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

// errNotForClient answers 422 not_for_client: the tab's figures exist only for Claude Code.
var errNotForClient = errors.New("figures exist only for claude-code")

func clientParam(c *api.Client) *string {
	if c == nil {
		return nil
	}
	return ptr(string(*c))
}

// claudeFigures says whether Claude-only figures apply under the filter: no filter, or claude-code.
func claudeFigures(c *api.Client) bool { return c == nil || *c == api.ClaudeCode }

func availability(ok bool) api.ClaudeOnly { return api.ClaudeOnly{Available: ok} }

// refuseClient validates the period first, so a bad period stays a 400 under any client.
func (s *Server) refuseClient(rng *api.TranscriptRange, from, to *openapi_types.Date, c *api.Client) error {
	if claudeFigures(c) {
		return nil
	}
	if _, err := api.ResolvePeriod(rng, from, to, s.now(), nil); err != nil {
		return err
	}
	return errNotForClient
}

// ContextLimitOf is ContextLimit for a Claude Code session and 0 for any other client, whose window is unknown.
func ContextLimitOf(client, model string) int64 {
	if client != string(clients.ClaudeCode) {
		return 0
	}
	return ContextLimit(model)
}

// clientList merges the detected clients with those that have stored rows, in clients.All order.
func clientList(detected []clients.ID, stored []string) []api.ClientInfo {
	found := map[clients.ID]bool{}
	for _, c := range detected {
		found[c] = true
	}
	for _, c := range stored {
		found[clients.ID(c)] = true
	}
	out := []api.ClientInfo{}
	for _, c := range clients.All() {
		if found[c] {
			out = append(out, api.ClientInfo{Id: api.Client(c), Verified: c.Verified()})
		}
	}
	return out
}

func (s *Server) detectedClients() []clients.ID {
	if s.Scanner == nil {
		return nil
	}
	return s.Scanner.Clients()
}
