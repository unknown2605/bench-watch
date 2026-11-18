package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
)

const (
	canaryWord = "CANARYcanary"
	mainUUID   = "c0ffee00-cafe-4abc-8def-0123456789ab"
	shapedMCP  = "mcp__CANARYserverABC123def456GHI789jkl__tool"
)

var (
	canaryPattern = regexp.MustCompile(`(?i)canary`)
	readLabel     = regexp.MustCompile(`^Read: (whole|ranged|image)$`)
	projectShape  = regexp.MustCompile(`^[A-Za-z0-9._ ()-]{1,120}$`)
	sessionShape  = regexp.MustCompile(`^(?:[0-9a-f]{8}|unknown)$`)
	hash16        = regexp.MustCompile(`^[0-9a-f]{16}$`)
	hash32        = regexp.MustCompile(`^[0-9a-f]{32}$`)
	shardKey      = regexp.MustCompile(`^claude-code-[0-9a-f]{32}$`)
	ids           = map[string]string{
		"message": "msg_CANARYraw_message_id", "shapedModel": "msg_CANARYraw_message_id2", "upperModel": "msg_CANARYraw_message_id3",
		"use": "toolu_CANARYraw_tool_use_id", "compact": "CANARYraw-compact-uuid", "recordUuid": "CANARYraw-record-uuid",
	}
)

func canary(name string) string { return canaryWord + "_" + name }

func allowedLabel(s string) bool {
	if rest, ok := strings.CutPrefix(s, "Bash: "); ok {
		return labels.Families[rest] || rest == "other" || rest == "script" || rest == "cd" || rest == "shell loop"
	}
	return readLabel.MatchString(s) || labels.ToolLabel(s) == s
}

func privacyFixture(b *box) (project string) {
	homeFolder := labels.HomePrefix()
	project = homeFolder + "-workspace-proj"
	everyRecord := rec{
		"cwd": "/Users/x/" + canary("cwd"), "gitBranch": canary("branch"), "slug": canary("slug"), "sessionId": mainUUID + canary("sid"),
		"requestId": canary("request"), "parentUuid": canary("parent"), "agentId": canary("agent_id"), "version": canary("version"),
		"entrypoint": canary("entrypoint"),
	}
	withMeta := func(r rec) rec {
		out := rec{"uuid": ids["recordUuid"]}
		for k, v := range everyRecord {
			out[k] = v
		}
		for k, v := range r {
			out[k] = v
		}
		return out
	}
	text := func(s string) rec { return rec{"type": "text", "text": s} }
	b.write(project+"/"+mainUUID+".jsonl",
		withMeta(rec{"type": "user", "timestamp": T(0), "message": rec{"role": "user", "content": canary("prompt")}}),
		withMeta(rec{"type": "user", "timestamp": T(0), "message": rec{"role": "user", "content": []any{text(canary("prompt_block"))}}}),
		rec{"type": "ai-title", "aiTitle": canary("title")}, rec{"type": "custom-title", "customTitle": canary("custom_title")},
		rec{"type": "last-prompt", "lastPrompt": canary("last_prompt")},
		rec{"type": "attachment", "attachment": rec{"content": canary("attachment")}},
		withMeta(call(ids["message"], 1, nil,
			text(canary("assistant_text")), rec{"type": "thinking", "thinking": canary("thinking")},
			toolUse(ids["use"], "Bash", rec{"command": canary("ENV") + "=hunter2 git commit -m " + canary("command"), "description": canary("description")}),
			toolUse("u2", "Bash", rec{"command": canary("binary") + " --flag"}),
			toolUse("u3", "Bash", rec{"command": "./" + canary("script") + ".sh"}),
			toolUse("u4", "Read", rec{"file_path": "/x/" + canary("path") + "/.env"}),
			toolUse("u5", "Grep", rec{"pattern": canary("pattern")}),
			toolUse("u6", "Write", rec{"file_path": "/x/" + canary("write"), "content": canary("file_content")}),
			toolUse("u7", "CANARYtool", rec{}),
			toolUse("u8", "WebFetch", rec{"url": "https://" + canary("url") + ".test/"}),
			toolUse("u9", shapedMCP, rec{}))),
		withMeta(withModel(call(ids["shapedModel"], 1, nil), "claude-canary1a2b3c4d5e6f7g8h9i0j1k2l3m")),
		withMeta(withModel(call(ids["upperModel"], 1, nil), "CANARYmodel-1")),
		withMeta(toolResult(ids["use"], 2, canary("stdout"), nil)),
		withMeta(toolResult("u2", 2, canary("secret_error"), rec{"is_error": true})),
		withMeta(toolResult("u3", 2, []any{text(canary("block_output"))}, nil)),
		withMeta(toolResult("u4", 2, "API_KEY="+canary("env_value"), nil)),
		withMeta(toolResult("u5", 2, []any{text(canary("grep_output")), text(canary("grep_output2"))}, nil)),
		withMeta(toolResult("u6", 2, canary("write_output"), nil)),
		withMeta(toolResult("u7", 2, canary("tool_output"), nil)),
		withMeta(toolResult("u8", 2, canary("web_output"), nil)),
		withMeta(toolResult("u9", 2, canary("mcp_output"), nil)),
		rec{"type": "system", "subtype": "compact_boundary", "uuid": ids["compact"], "timestamp": T(60), "content": canary("compact_note"),
			"compactMetadata": rec{"trigger": canary("trigger"), "preTokens": 5}},
		rec{"type": "user", "isCompactSummary": true, "timestamp": T(61), "message": rec{"role": "user", "content": canary("summary")}},
		rec{"type": "pr-link", "prUrl": canary("pr")})
	named := func(id, rel, agentType string) {
		b.write(rel, withMeta(call(id, 3, nil)))
		if agentType != "" {
			b.writeMeta(rel, rec{"agentType": agentType, "description": canary("meta_description")})
		}
	}
	named("f1", "-Users-canary-CANARYoutside_home/d1111111-0000-4000-8000-000000000000.jsonl", "")
	named("f2", homeFolder+"-workspace-CANARYabc123def456ghi789jkl012/d2222222-0000-4000-8000-000000000000.jsonl", "")
	named("f3", project+"--claude-worktrees-CANARYworktree/d3333333-0000-4000-8000-000000000000.jsonl", "")
	named("f4", project+"/CANARYfile.jsonl", "")
	named("s1", project+"/"+mainUUID+"/subagents/agent-CANARYsub.jsonl", "Canary7Agent9Type4")
	named("s2", project+"/CANARYsession/subagents/agent-a.jsonl", "CANARYabcDEF123ghiJKL456mnoPQR789")
	named("s3", project+"/"+mainUUID+"/subagents/agent-b.jsonl", "sk-ant-CANARY")
	return project
}

func generatedFiles(t *testing.T, b *box) []string {
	files := []string{b.out}
	must(t, filepath.WalkDir(b.cache, func(path string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			files = append(files, path)
		}
		return err
	}))
	return files
}

func assertNoLeak(t *testing.T, where string, text []byte) {
	t.Helper()
	home, _ := os.UserHomeDir()
	fatalf(t, !canaryPattern.Match(text), "leak in %s", where)
	fatalf(t, !bytes.Contains(text, []byte(mainUUID[8:])), "session UUID beyond the 8-character prefix in %s", where)
	fatalf(t, !bytes.Contains(text, []byte(home)), "home path in %s", where)
}

func TestNoCanaryReachesTheDumpTheCacheOrTheLogs(t *testing.T) {
	b := sandbox(t)
	privacyFixture(b)
	c := b.runChild()
	equal(t, c.exit, 0, "exit")
	d := data(readJSON(t, b.out))
	equal(t, d.stat("uniqueCalls"), 10, "the fixture was really processed")
	fatalf(t, len(d.strings("labels")) > 3, "labels")
	equal(t, strings.Join(d.strings("agentTypes"), ","), "unknown", "all three shaped agent types were rejected")
	models := d.strings("models")
	sort.Strings(models)
	equal(t, strings.Join(models, ","), "claude-opus-5-5,unknown", "models")
	hasOther := false
	for _, l := range d.strings("labels") {
		hasOther = hasOther || l == "other"
		fatalf(t, !canaryPattern.MatchString(l), "a canary label")
	}
	fatalf(t, hasOther, "no other label")
	fatalf(t, strings.Contains(strings.Join(d.strings("projects"), "\n"), "other"), "no other project")
	files := generatedFiles(t, b)
	kinds := map[string]bool{}
	for _, f := range files {
		kinds[filepath.Base(f)] = true
		if strings.Contains(f, "/shards/") {
			kinds["shard"] = true
		}
		text, err := os.ReadFile(f)
		must(t, err)
		assertNoLeak(t, filepath.Base(f), text)
	}
	fatalf(t, kinds["index.json"] && kinds["status.json"] && kinds["output.json"] && kinds["shard"], "files %v", kinds)
	assertNoLeak(t, "stdout", []byte(c.stdout))
	assertNoLeak(t, "stderr", []byte(c.stderr))
	again := b.runChild()
	assertNoLeak(t, "idle stdout", []byte(again.stdout+again.stderr))
}

func TestTheCacheHoldsHashesAndLabelsOnly(t *testing.T) {
	b := sandbox(t)
	privacyFixture(b)
	b.scan()
	files := readJSON(t, b.indexFile())["files"].(map[string]any)
	shards, _ := filepath.Glob(filepath.Join(b.cache, "shards", "*"))
	equal(t, len(shards), len(files), "one shard per index entry")
	for key, entry := range files {
		fatalf(t, shardKey.MatchString(key), "index key")
		equal(t, strings.Join(statusKeys(entry.(map[string]any)), ","), "birthMs,firstTs,mtimeMs,shardBytes,shardHash,size,sub", "entry keys")
	}
	events := 0
	var mainShard map[string]any
	for _, file := range shards {
		fatalf(t, regexp.MustCompile(`^claude-code-[0-9a-f]{32}\.json$`).MatchString(filepath.Base(file)), "shard name")
		shard := readJSON(t, file)
		equal(t, strings.Join(statusKeys(shard), ","),
			"agentType,client,events,key,lastTs,lines,mtimeMs,offset,pending,project,scope,session,size,skipped,v", "shard keys")
		fatalf(t, projectShape.MatchString(shard["project"].(string)), "project shape")
		fatalf(t, sessionShape.MatchString(shard["session"].(string)), "session shape")
		for _, p := range shard["pending"].([]any) {
			pair := p.([]any)
			fatalf(t, hash16.MatchString(pair[0].(string)) && allowedLabel(pair[1].(string)), "pending entry")
		}
		for _, e := range shard["events"].([]any) {
			ev := e.([]any)
			fatalf(t, hash16.MatchString(ev[2].(string)), "an event ID that is not a hash")
			events++
		}
		text, _ := os.ReadFile(file)
		if bytes.Contains(text, []byte(extract.HashID(ids["use"]))) {
			mainShard = shard
		}
	}
	fatalf(t, events > 10, "events %d", events)
	fatalf(t, mainShard != nil, "the main shard holds the hashed tool use ID")
	found := map[string]bool{}
	for _, e := range mainShard["events"].([]any) {
		found[e.([]any)[2].(string)] = true
	}
	fatalf(t, found[extract.HashID(ids["message"])] && found[extract.HashID(ids["compact"])], "hashed IDs missing")
}

func TestAFailureLogsAndRecordsAnErrorCodeOnly(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.scan()
	must(t, os.MkdirAll(filepath.Join(b.dir, "CANARYcache"), 0o700))
	must(t, os.WriteFile(filepath.Join(b.dir, "CANARYcache/blocker"), nil, 0o600))
	c := b.runChild(b.out, filepath.Join(b.dir, "CANARYcache/blocker/sub"))
	equal(t, c.exit, 1, "exit")
	fatalf(t, regexp.MustCompile(`^scan failed: [A-Z]+\n$`).MatchString(c.stderr), "stderr %q", c.stderr)
	fatalf(t, !canaryPattern.MatchString(c.stderr+c.stdout) && !strings.Contains(c.stderr, b.dir), "a path in the log")

	blocked := filepath.Join(b.dir, "CANARYout")
	must(t, os.MkdirAll(blocked, 0o755))
	appendTo(t, filepath.Join(b.root, mainRel), call("m2", 2, nil))
	c = b.runChild(blocked, b.cache)
	equal(t, c.exit, 1, "exit")
	fatalf(t, !canaryPattern.MatchString(c.stderr+c.stdout) && !strings.Contains(c.stderr, b.dir), "a path in the log")
	status := readJSON(t, filepath.Join(b.cache, "status.json"))
	equal(t, strings.Join(statusKeys(status), ","), "code,lastRunAt,ok", "status keys")
	fatalf(t, regexp.MustCompile(`^[A-Z]+$`).MatchString(status["code"].(string)) && status["ok"] == false, "status %v", status)
}

func TestEveryStringInTheDumpIsAKnownFieldOrALabelOfAnAllowedShape(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel,
		call("m1", 1, nil, toolUse("u1", "Bash", rec{"command": "weird-command --x"}), toolUse("u2", "Read", rec{"file_path": "/a"})),
		toolResult("u1", 2, "out", nil), toolResult("u2", 2, "out", nil))
	d := b.scan().data
	for _, l := range d.strings("labels") {
		fatalf(t, allowedLabel(l), "label %q", l)
	}
	for _, p := range d.strings("projects") {
		fatalf(t, projectShape.MatchString(p), "project %q", p)
	}
	for _, m := range d.strings("models") {
		fatalf(t, m == "unknown" || labels.ModelLabel(m) == m, "model %q", m)
	}
	for _, s := range d["sessions"].([]any) {
		fatalf(t, sessionShape.MatchString(s.([]any)[0].(string)), "session")
	}
	text, _ := json.Marshal(d)
	assertNoLeak(t, "dump", text)
}
