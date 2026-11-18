// Package clients names the coding agents whose usage records the scanner can read (CM-00016). It imports nothing
// of this module, so labels, pricing and source can all depend on it.
package clients

import "fmt"

// ID is a client as stored in the client column and served by the API; "tool" already means a built-in tool.
type ID string

const (
	ClaudeCode ID = "claude-code"
	Codex      ID = "codex"
	OpenCode   ID = "opencode"
	GeminiCLI  ID = "gemini-cli"
	QwenCode   ID = "qwen-code"
	CopilotCLI ID = "copilot-cli"
)

// The order is the one the API lists clients in; the database check in migration 00009 holds the same set.
var all = []ID{ClaudeCode, Codex, OpenCode, GeminiCLI, QwenCode, CopilotCLI}

func All() []ID { return append([]ID(nil), all...) }

func (id ID) Valid() bool {
	for _, c := range all {
		if c == id {
			return true
		}
	}
	return false
}

// Verified says the reader was checked against this client's real records; the others follow upstream source only.
func (id ID) Verified() bool { return id == ClaudeCode || id == Codex }

// Parse refuses any name outside the list, so a typo never becomes a new client.
func Parse(s string) (ID, error) {
	if id := ID(s); id.Valid() {
		return id, nil
	}
	return "", fmt.Errorf("unknown client %q", s)
}
