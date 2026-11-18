package labels

import (
	"regexp"

	"benchwatch/server/internal/clients"
)

// ModelPattern is the schema's ModelLabel pattern (api/openapi.yaml, is_model_label): dot, dash, underscore or colon
// between lowercase words, never two in a row, never a slash, so no path or sentence passes.
const ModelPattern = `^[a-z0-9]{1,24}(?:[._:-][a-z0-9]{1,24}){0,11}(?:\[[0-9]{1,2}[a-z]\])?$`

const ModelMaxLength = 60

var safeModel = regexp.MustCompile(ModelPattern)

// IsModel answers whether v passes the schema's model check, whatever the client.
func IsModel(v string) bool { return len(v) <= ModelMaxLength && safeModel.MatchString(v) }

// Model id shapes per client, conservative: an id outside its client's families is stored as "unknown" and its calls
// still count there. A bare family name ("codex", "gemini") is kept as written; pricing lists it as unpriced.
const (
	openAIModel  = `gpt-[a-z0-9][a-z0-9.]{0,7}(?:-[a-z0-9.]{1,16}){0,4}|o[0-9]{1,2}(?:-[a-z0-9]{1,16}){0,3}|` + codexModel
	codexModel   = `codex(?:-[a-z0-9.]{1,16}){0,4}`
	geminiModel  = `gemini(?:-[a-z0-9.]{1,8}(?:-[a-z0-9.]{1,16}){0,5})?`
	qwenModel    = `qwen[a-z0-9.]{0,8}(?:-[a-z0-9.]{1,16}){0,5}`
	claudeDotted = `claude-[a-z]{2,16}(?:-[0-9]{1,2}(?:\.[0-9]{1,2})?){1,3}(?:-[0-9]{8})?|` +
		`claude-[0-9](?:[.-][0-9]){0,2}-[a-z]{2,16}(?:-[0-9]{8})?`
)

func anyOf(shapes ...string) *regexp.Regexp {
	alt := ""
	for i, s := range shapes {
		if i > 0 {
			alt += "|"
		}
		alt += s
	}
	return regexp.MustCompile(`^(?:` + alt + `)$`)
}

var clientModels = map[clients.ID]*regexp.Regexp{
	clients.Codex:      anyOf(openAIModel),
	clients.GeminiCLI:  anyOf(geminiModel),
	clients.QwenCode:   anyOf(qwenModel),
	clients.CopilotCLI: anyOf(claudeDotted, openAIModel, geminiModel),
	clients.OpenCode:   anyOf(claudeDotted, openAIModel, geminiModel, qwenModel),
}

// Model returns the model label of a call recorded by client, or "unknown".
func Model(client clients.ID, model any) string {
	if client == clients.ClaudeCode {
		return ModelLabel(model)
	}
	text, ok := model.(string)
	re := clientModels[client]
	if ok && re != nil && IsModel(text) && re.MatchString(text) && !LooksSecret(text) {
		return text
	}
	return "unknown"
}
