package labels

import (
	"strings"
	"testing"

	"benchwatch/server/internal/clients"
)

func TestIsModelRefusesPathsAndText(t *testing.T) {
	for _, v := range []string{"claude-opus-5-5", "claude-sonnet-4-6[1m]", "gpt-5.1-codex-max", "o4-mini", "unknown",
		"qwen3-coder-plus", "gemini-2.5-pro", "a_b:c"} {
		if !IsModel(v) {
			t.Errorf("%q refused", v)
		}
	}
	for _, v := range []string{"", "/Users/x", "users/x", "a/b", "../x", "x/../y", ".env", "~", "a..b", "a-", "-a",
		"gpt says hi", "GPT-4", "a\nb", "c:\\x", "x" + strings.Repeat("-a", 30), "model[1m]x", "café"} {
		if IsModel(v) {
			t.Errorf("%q accepted", v)
		}
	}
}

func TestModelPerClient(t *testing.T) {
	cases := []struct {
		client clients.ID
		ok     []string
		bad    []string
	}{
		{clients.ClaudeCode, []string{"claude-opus-5-5", "claude-haiku-4-5-20251001"}, []string{"gpt-5", "claude-"}},
		{clients.Codex, []string{"gpt-5", "gpt-5-codex", "gpt-5.1-codex-max", "gpt-4o", "gpt-4.1-mini", "o3", "o4-mini",
			"codex-mini-latest", "gpt-oss-120b", "codex"}, []string{"claude-opus-5-5", "gemini-2.5-pro", "hello-world", "gpt-",
			"codex-"}},
		{clients.GeminiCLI, []string{"gemini-2.5-pro", "gemini-2.5-flash-lite", "gemini-3-pro-preview", "gemini"},
			[]string{"gpt-5", "qwen3-coder-plus", "gemini-"}},
		{clients.QwenCode, []string{"qwen3-coder-plus", "qwen-max", "qwen2.5-coder-32b-instruct"},
			[]string{"gemini-2.5-pro", "coder-model"}},
		{clients.CopilotCLI, []string{"claude-sonnet-4.5", "claude-haiku-4.5", "gpt-5", "gpt-5-mini", "gemini-3-pro-preview"},
			[]string{"qwen3-coder-plus", "my-model"}},
		{clients.OpenCode, []string{"claude-sonnet-4-5", "claude-opus-4.1", "gpt-5", "gemini-2.5-pro", "qwen3-coder"},
			[]string{"anthropic/claude-sonnet-4-5", "big-pickle", "llama-3"}},
	}
	for _, c := range cases {
		for _, v := range c.ok {
			if got := Model(c.client, v); got != v {
				t.Errorf("%s %q: got %q", c.client, v, got)
			}
		}
		for _, v := range append(c.bad, "", "/Users/x/gpt-5", "gpt-5 says hi", "gpt-5\n", "../gpt-5") {
			if got := Model(c.client, v); got != "unknown" {
				t.Errorf("%s %q: got %q, want unknown", c.client, v, got)
			}
		}
		if got := Model(c.client, 42); got != "unknown" {
			t.Errorf("%s non-string: got %q", c.client, got)
		}
	}
	if got := Model(clients.ID("cursor"), "gpt-5"); got != "unknown" {
		t.Errorf("unlisted client: got %q", got)
	}
	for _, c := range clients.All() {
		for _, v := range []string{"claude-opus-5-5", "gpt-5.1-codex-max", "gemini-2.5-pro", "qwen3-coder-plus"} {
			if got := Model(c, v); got != "unknown" && !IsModel(got) {
				t.Errorf("%s %q: label %q fails the schema check", c, v, got)
			}
		}
	}
}
