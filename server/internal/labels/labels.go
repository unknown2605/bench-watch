// Package labels is the privacy boundary: anything from a transcript reaches the output only through it.
package labels

import (
	"os"
	"regexp"
	"strings"
)

func wordSet(text string) map[string]bool {
	set := map[string]bool{}
	for _, w := range strings.Fields(text) {
		set[w] = true
	}
	return set
}

var Families = wordSet(`git gh docker docker-compose kubectl helm npm npx pnpm yarn bun node deno tsc tsx vitest jest
eslint biome prettier playwright python python3 pip pip3 uv uvx pytest ruff mypy ruby gem bundle php composer go
gofmt cargo rustc make cmake curl wget http ssh scp rsync sftp aws gcloud az sls serverless shopify terraform
wrangler cloudflared jq yq sed awk grep egrep rg find fd ls cat head tail less wc sort uniq cut tr tee xargs diff cmp
comm stat file du df ps top lsof netstat kill pkill pgrep nc dig ping mkdir rmdir rm cp mv touch chmod chown ln
readlink realpath basename dirname tar zip unzip gzip gunzip base64 shasum sha1sum sha256sum md5 md5sum openssl date
sleep echo printf pwd which whoami id uname hostname env test true false bash sh zsh open osascript launchctl sysctl
vm_stat sw_vers defaults plutil pbcopy pbpaste brew nvm pm2 nginx mysql mysqldump mariadb psql pg_dump redis-cli
sqlite3 dphp dnode dsql dredis stack-doctor magento-cloud artisan ffmpeg ffprobe convert magick sips identify
exiftool strings xxd od hexdump nl seq paste column bc expr yes wait type command perl gofumpt golangci-lint
staticcheck govulncheck semgrep gitleaks trivy`)

var (
	shellControl = wordSet("for while until if case select function { (")
	skipSegment  = wordSet("cd pushd popd export unset set source . trap local declare ulimit umask")
	wrappers     = wordSet("sudo time nice nohup exec builtin caffeinate")
	assignment   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// Anything past this cap is not needed to name the first command, in UTF-16 units as in the Node scanner.
const maxCommandChars = 16 * 1024

func stripAssignment(text string) string {
	rest := text[len(assignment.FindString(text)):]
	if rest != "" && (rest[0] == '"' || rest[0] == '\'') {
		end := strings.IndexByte(rest[1:], rest[0])
		if end < 0 {
			return ""
		}
		return TrimStartJS(rest[end+2:])
	}
	space := indexJSSpace(rest)
	if space < 0 {
		return ""
	}
	return TrimStartJS(rest[space:])
}

func leadingWord(text string) string {
	if i := indexJSSpace(text); i >= 0 {
		return text[:i]
	}
	return text
}

// stripFlags is text.replace(/^(-\S+\s+)*/, ""): every group needs a non-space run after '-' and then space.
func stripFlags(text string) string {
	for len(text) > 1 && text[0] == '-' {
		word := leadingWord(text[1:])
		if word == "" {
			break
		}
		after := text[1+len(word):]
		rest := TrimStartJS(after)
		if rest == after {
			break
		}
		text = rest
	}
	return text
}

// stripTimeout is rest.replace(/^(-\S+\s+)*\d+\S*\s*/, ""); without the number nothing is removed.
func stripTimeout(rest string) string {
	text := stripFlags(rest)
	if text == "" || text[0] < '0' || text[0] > '9' {
		return rest
	}
	return TrimStartJS(text[len(leadingWord(text)):])
}

func headOf(segment string) string {
	text := TrimJS(segment)
	for guard := 0; guard < 12 && text != ""; guard++ {
		if assignment.MatchString(text) {
			text = stripAssignment(text)
			continue
		}
		word := leadingWord(text)
		rest := TrimStartJS(text[len(word):])
		switch {
		case wrappers[word]:
			text = stripFlags(rest)
		case word == "env":
			text = stripFlags(rest)
			if !assignment.MatchString(text) {
				return "env"
			}
		case word == "timeout":
			text = stripTimeout(rest)
		default:
			return word
		}
	}
	return ""
}

// splitCommand is command.split(/&&|\|\||[;|\n]/).slice(0, limit).
func splitCommand(s string, limit int) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s) && len(parts) < limit; {
		width := 0
		if i+1 < len(s) && ((s[i] == '&' && s[i+1] == '&') || (s[i] == '|' && s[i+1] == '|')) {
			width = 2
		} else if s[i] == ';' || s[i] == '|' || s[i] == '\n' {
			width = 1
		}
		if width == 0 {
			i++
			continue
		}
		parts = append(parts, s[start:i])
		i += width
		start = i
	}
	if len(parts) < limit {
		parts = append(parts, s[start:])
	}
	return parts
}

// BashFamily names the first real command as an allowlisted family, never as a raw word.
func BashFamily(command any) string {
	text, ok := command.(string)
	if !ok || TrimJS(text) == "" {
		return "Bash: other"
	}
	sawSkip := false
	for _, raw := range splitCommand(UTF16Prefix(text, maxCommandChars), 25) {
		segment := TrimJS(raw)
		if segment == "" || segment[0] == '#' {
			continue
		}
		word := headOf(segment)
		if word == "" {
			continue
		}
		if skipSegment[word] {
			sawSkip = true
			continue
		}
		if shellControl[word] {
			return "Bash: shell loop"
		}
		slash := strings.LastIndexByte(word, '/')
		if slash < 0 {
			if Families[word] {
				return "Bash: " + word
			}
			return "Bash: other"
		}
		if name := word[slash+1:]; Families[name] {
			return "Bash: " + name
		}
		return "Bash: script"
	}
	if sawSkip {
		return "Bash: cd"
	}
	return "Bash: other"
}

const hexClass = `[0-9a-fA-F]`

var (
	uuidPattern = regexp.MustCompile(hexClass + `{8}-` + hexClass + `{4}-` + hexClass + `{4}-` + hexClass + `{4}-` +
		hexClass + `{12}`)
	keyPrefix = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(?:sk-(?:ant|proj|or|live|test)-|sk-[A-Za-z0-9]{16}|` +
		`(?:sk|pk|rk)_(?:live|test)_|gh[pousr]_|github_pat_|glpat-|xox[abeoprs]-|(?:AKIA|ASIA)[0-9A-Z]{12}|` +
		`AIza[0-9A-Za-z_-]{10}|ya29\.|eyJ[A-Za-z0-9_-]{6}|` +
		`npm_[A-Za-z0-9]{10}|shp(?:at|ss|ca|pa|tka)_|SG\.[A-Za-z0-9_-]{10}|hf_[A-Za-z0-9]{10}|pypi-[A-Za-z0-9]{10}|dop_v1_)`)
	ordinaryName = regexp.MustCompile(`^[a-z]*(?:[A-Z][a-z]{2,})*[A-Z]?[0-9]{1,4}[a-z]*(?:[A-Z][a-z]{2,})*$`)
	// Spelled out without (?i): RE2 folds k with U+212A and s with U+017F, which JavaScript's /i does not.
	consonantRun = regexp.MustCompile(`[b-df-hj-np-tv-xzB-DF-HJ-NP-TV-XZ]{4}`)
	choppyPair   = regexp.MustCompile(`[A-Z]{2}[a-z]+[A-Z]{2}`)
)

const (
	longMixed      = 8
	longCased      = 24
	longSingleCase = 33
	longHex        = 16
	longDigits     = 12
	// base64url tokens carry - and _, so a split token shows as several short noisy parts; real labels hold none.
	noiseLimit  = 8
	noiseFrom   = 3
	ordinaryMax = 24
)

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func has(s string, test func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if test(s[i]) {
			return true
		}
	}
	return false
}

func all(s string, test func(byte) bool) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !test(s[i]) {
			return false
		}
	}
	return true
}

func isHex(c byte) bool      { return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') }
func isLowerHex(c byte) bool { return isDigit(c) || (c >= 'a' && c <= 'f') }
func isLetter(c byte) bool   { return isLower(c) || isUpper(c) }

// choppyCase is /(?:^|[A-Z])[a-z](?=[A-Z]|$)|[A-Z]{2}[a-z]+[A-Z]{2}/ with the lookahead written out for RE2.
func choppyCase(s string) bool {
	for i := 0; i < len(s); i++ {
		if isLower(s[i]) && (i == 0 || isUpper(s[i-1])) && (i+1 == len(s) || isUpper(s[i+1])) {
			return true
		}
	}
	return choppyPair.MatchString(s)
}

func isOrdinaryName(part string) bool {
	return UTF16Len(part) <= ordinaryMax && ordinaryName.MatchString(part) && !all(part, isHex) &&
		!consonantRun.MatchString(part)
}

func isNoisy(part string) bool {
	lower, upper := has(part, isLower), has(part, isUpper)
	if has(part, isDigit) {
		return UTF16Len(part) < longMixed && (upper || (lower && all(part, isLowerHex)))
	}
	return upper && lower && choppyCase(part)
}

func isSecretPart(part string) bool {
	n := UTF16Len(part)
	if n >= longHex && all(part, isHex) {
		return true
	}
	digit, letter := has(part, isDigit), has(part, isLetter)
	if digit && letter {
		return n >= longMixed && !isOrdinaryName(part)
	}
	if digit {
		return n >= longDigits
	}
	if n >= longSingleCase {
		return true
	}
	return n >= longCased && has(part, isLower) && has(part, isUpper)
}

func isSeparator(c byte) bool { return c == '-' || c == '_' || c == '.' || c == ':' }

// splitParts is value.split(/[-_.:]+/), empty parts included.
func splitParts(value string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(value); {
		if !isSeparator(value[i]) {
			i++
			continue
		}
		parts = append(parts, value[start:i])
		for i < len(value) && isSeparator(value[i]) {
			i++
		}
		start = i
	}
	return append(parts, value[start:])
}

// KeyOrUUID is LooksSecret's key prefix and UUID check alone, for a gate that reads word shapes itself.
func KeyOrUUID(value string) bool {
	return keyPrefix.MatchString(value) || uuidPattern.MatchString(value)
}

// LooksSecret is the one check every label gate shares, so a token shape is rejected everywhere or nowhere.
func LooksSecret(value string) bool {
	if KeyOrUUID(value) {
		return true
	}
	noise := 0
	for _, part := range splitParts(value) {
		if isSecretPart(part) {
			return true
		}
		if n := UTF16Len(part); n >= noiseFrom && isNoisy(part) {
			noise += n
		}
	}
	return noise >= noiseLimit
}

// Built-in tools counted in real transcripts on 2026-11-04, plus the documented ones not used yet. Add new ones here.
var tools = wordSet(`Agent Artifact AskUserQuestion Bash bash BashOutput Edit EnterPlanMode ExitPlanMode Glob Grep
KillShell ListAgents ListMcpResourcesTool ListPlugins LS Monitor MultiEdit NotebookEdit Read ReadMcpResourceTool
ReadNotifications ScheduleWakeup SearchPlugins SendMessage SendUserFile Skill SubagentHandback SuggestPluginInstall
Task TaskCreate TaskGet TaskList TaskOutput TaskStop TaskUpdate TodoWrite ToolSearch WebFetch WebSearch Write`)

var mcpTool = regexp.MustCompile(`^mcp__[A-Za-z0-9_-]{1,60}__[A-Za-z0-9_-]{1,60}$`)

const maxLabelInput = 200

func ToolLabel(name any) string {
	text, ok := name.(string)
	if !ok || UTF16Len(text) > maxLabelInput {
		return "other"
	}
	if tools[text] {
		return text
	}
	clean := uuidPattern.ReplaceAllLiteralString(text, "connector")
	if UTF16Len(clean) <= 80 && mcpTool.MatchString(clean) && !LooksSecret(clean) {
		return clean
	}
	return "other"
}

// ResultLabel takes the tool_use name and input as parsed JSON values; an object is a map[string]any.
func ResultLabel(name, input any) string {
	fields, _ := input.(map[string]any)
	switch name {
	case "Bash":
		return BashFamily(fields["command"])
	case "Read":
		_, offset := fields["offset"]
		_, limit := fields["limit"]
		_, pages := fields["pages"]
		if offset || limit || pages {
			return "Read: ranged"
		}
		return "Read: whole"
	}
	return ToolLabel(name)
}

var (
	modelNew = regexp.MustCompile(`^claude-[a-z]{2,16}(?:-[0-9]{1,2}){1,3}(?:-[0-9]{8})?(?:-latest)?` +
		`(?:\[[0-9]{1,2}[a-z]\])?$`)
	modelOld = regexp.MustCompile(`^claude-[0-9](?:-[0-9]){0,2}-[a-z]{2,16}(?:-[0-9]{8}|-latest)?` +
		`(?:\[[0-9]{1,2}[a-z]\])?$`)
	contextTag = regexp.MustCompile(`\[[0-9a-z]+\]$`)
	agentShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,59}$`)
)

func ModelLabel(model any) string {
	text, ok := model.(string)
	if ok && UTF16Len(text) <= 60 && (modelNew.MatchString(text) || modelOld.MatchString(text)) &&
		!LooksSecret(contextTag.ReplaceAllLiteralString(text, "")) {
		return text
	}
	return "unknown"
}

func AgentLabel(agentType any) string {
	text, ok := agentType.(string)
	if ok && agentShape.MatchString(text) && !LooksSecret(text) {
		return text
	}
	return "unknown"
}

// HomePrefix is homedir().replace(/[^A-Za-z0-9]/g, '-'), which replaces per UTF-16 unit.
func HomePrefix() string {
	home, _ := os.UserHomeDir()
	var b strings.Builder
	for _, r := range home {
		switch {
		case r < 0x80 && (isLetter(byte(r)) || isDigit(byte(r))):
			b.WriteRune(r)
		case r >= 0x10000:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// cutTail is label.replace(new RegExp(marker + '.*$'), ""): JavaScript's dot stops at line terminators.
func cutTail(label, marker string) string {
	for from := 0; ; {
		i := strings.Index(label[from:], marker)
		if i < 0 {
			return label
		}
		at := from + i
		if !strings.ContainsFunc(label[at+len(marker):], LineTerminator) {
			return label[:at]
		}
		from = at + 1
	}
}

// cutWorktrees is label.replace(/-\.?worktrees-.*$/, "").
func cutWorktrees(label string) string {
	for from := 0; ; {
		i := strings.IndexByte(label[from:], '-')
		if i < 0 {
			return label
		}
		at := from + i
		rest := label[at+1:]
		rest = strings.TrimPrefix(rest, ".")
		if strings.HasPrefix(rest, "worktrees-") && !strings.ContainsFunc(rest[len("worktrees-"):], LineTerminator) {
			return label[:at]
		}
		from = at + 1
	}
}

var projectShape = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}$`)

// ProjectLabel names a project folder only inside the home path; home is HomePrefix() outside tests.
func ProjectLabel(folder, home string) string {
	label := folder
	if strings.HasPrefix(label, "-private-tmp") || strings.HasPrefix(label, "-tmp") {
		return "scratch (tmp)"
	}
	if label == home {
		return "home"
	}
	switch {
	case strings.HasPrefix(label, home+"-workspace-"):
		label = label[len(home)+11:]
	case strings.HasPrefix(label, home+"-"):
		label = label[len(home)+1:]
	default:
		return "other"
	}
	label = strings.TrimLeft(cutWorktrees(cutTail(label, "--claude-worktrees-")), "-")
	if projectShape.MatchString(label) && !LooksSecret(label) {
		return label
	}
	return "other"
}

var (
	// Exact forms counted on real transcripts, 2026-11-04. Hook blocks also carry toolDenialKind, so check them first.
	hookBlock = regexp.MustCompile(`^(?:<tool_use_error>)?(?:Pre|Post)ToolUse:[^` + JSSpaceClass + `]+ hook error:`)
	denial    = regexp.MustCompile(`^(?:The user doesn't want to proceed with this tool use|` +
		`Permission to use [^` + JSSpaceClass + `]+ has been denied|Permission for this action has been denied)`)
	exitCode    = regexp.MustCompile(`^Exit code [0-9]+(\nCommand timed out)?`)
	toolMissing = regexp.MustCompile(`^<tool_use_error>(?:String to replace not found|File does not exist)`)
)

// asciiLower stands in for toLowerCase: only U+0130 and U+212A lower into ASCII, and neither completes a phrase here.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if isUpper(c) {
			b[i] = c + 32
		}
	}
	return string(b)
}

func ErrorClass(denied bool, text string) string {
	head := UTF16Prefix(text, 300)
	if hookBlock.MatchString(head) {
		return "blocked by hook"
	}
	if denied || denial.MatchString(head) {
		return "denied"
	}
	if m := exitCode.FindStringSubmatchIndex(head); m != nil {
		if m[2] >= 0 {
			return "timeout"
		}
		return "non-zero exit"
	}
	if strings.HasPrefix(head, "<tool_use_error>") {
		if toolMissing.MatchString(head) {
			return "not found"
		}
		return "other"
	}
	lower := asciiLower(head)
	if strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") {
		return "timeout"
	}
	if strings.Contains(lower, "no such file") || strings.Contains(lower, "not found") ||
		strings.Contains(lower, "does not exist") {
		return "not found"
	}
	return "other"
}
