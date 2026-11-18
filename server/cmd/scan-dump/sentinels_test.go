package main

import (
	"encoding/base64"
	"encoding/binary"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/labels"
)

// Every placeholder value the scanner can emit instead of a real name. CM-00002's CHECK constraints must accept each;
// a change here is a change to that contract.
var sentinels = map[string][]string{
	"models":     {"unknown"},
	"agentTypes": {"unknown"},
	"sessions":   {"unknown"},
	"projects":   {"other", "home", "scratch (tmp)"},
	"labels": {"other", "Read: image", "Read: whole", "Read: ranged", "Bash: other", "Bash: script", "Bash: cd",
		"Bash: shell loop"},
	"errorClasses": {"blocked by hook", "denied", "timeout", "non-zero exit", "not found", "other"},
}

func sorted(v []string) []string {
	out := append([]string(nil), v...)
	sort.Strings(out)
	return out
}

func TestEverySentinelTheScannerCanEmitIsListed(t *testing.T) {
	b := sandbox(t)
	png := make([]byte, 40000)
	binary.BigEndian.PutUint32(png[0:], 0x89504e47)
	binary.BigEndian.PutUint32(png[16:], 1500)
	binary.BigEndian.PutUint32(png[20:], 1000)
	image := []any{rec{"type": "image", "source": rec{"type": "base64", "media_type": "image/png",
		"data": base64.StdEncoding.EncodeToString(png)}}}
	failing := func(id, text string) rec { return toolResult(id, 3, text, rec{"is_error": true}) }
	b.write("-Users-x-workspace-proj/not-a-session.jsonl",
		withModel(call("m1", 1, nil,
			toolUse("a", "Read", rec{"file_path": "/a"}), toolUse("b", "Read", rec{"offset": 1}), toolUse("c", "Read", rec{}),
			toolUse("d", "Bash", rec{"command": "ls"}), toolUse("e", "Bash", rec{"command": "./run.sh"}),
			toolUse("f", "Bash", rec{"command": "cd /x"}), toolUse("g", "Bash", rec{"command": "for i in 1; do ls; done"}),
			toolUse("h", strings.Repeat("N", 300), rec{}),
			toolUse("i", "Bash", rec{"command": "x"}), toolUse("j", "Bash", rec{"command": "y"}),
			toolUse("k", "Bash", rec{"command": "z"}),
			toolUse("l", "Bash", rec{"command": "w"}), toolUse("m", "Bash", rec{"command": "v"}),
			toolUse("n", "Bash", rec{"command": "u"})),
			"not a model"),
		toolResult("a", 2, "12345678", nil), toolResult("b", 2, "12345678", nil), toolResult("c", 2, image, nil),
		toolResult("d", 2, "x", nil), toolResult("e", 2, "x", nil), toolResult("f", 2, "x", nil),
		toolResult("g", 2, "x", nil),
		toolResult("h", 2, "x", nil),
		failing("i", "PreToolUse:Bash hook error: x"),
		with(failing("j", "rejected"), rec{"toolDenialKind": "user-rejected"}),
		failing("k", "Exit code 1\nCommand timed out"),
		failing("l", "Exit code 2\nfailed"),
		failing("m", "<tool_use_error>File does not exist</tool_use_error>"),
		failing("n", "something else"))
	b.write(subRel, call("s1", 4, nil))
	d := b.scan().data

	got := map[string][]string{}
	got["models"] = d.strings("models")
	got["agentTypes"] = d.strings("agentTypes")
	for _, s := range d["sessions"].([]any) {
		got["sessions"] = append(got["sessions"], s.([]any)[0].(string))
	}
	got["projects"] = d.strings("projects")
	got["labels"] = d.strings("labels")
	got["errorClasses"] = d.strings("errorClasses")

	listed := func(dim string, real ...string) []string {
		return append(append([]string(nil), sentinels[dim]...), real...)
	}
	want := map[string][]string{
		"models":       listed("models", "claude-opus-5-5"),
		"agentTypes":   listed("agentTypes"),
		"sessions":     listed("sessions", "aaaaaaaa"),
		"projects":     {"other"},
		"labels":       listed("labels", "Bash: ls"),
		"errorClasses": listed("errorClasses"),
	}
	for dim, list := range want {
		fatalf(t, reflect.DeepEqual(sorted(got[dim]), sorted(list)), "%s emitted %v", dim, got[dim])
	}
	fatalf(t, contains(sentinels["projects"], "other"), "projects lacks its sentinel %q", "other")
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestProjectSentinelsFollowTheHomePath(t *testing.T) {
	home := "-Users-a"
	emitted := map[string]bool{}
	for folder, want := range map[string]string{"-private-tmp-x": "scratch (tmp)", "-tmp-x": "scratch (tmp)", home: "home",
		"-opt-x": "other", home + "-workspace-app": "app"} {
		got := labels.ProjectLabel(folder, home)
		equal(t, got, want, "project label")
		if got != "app" {
			emitted[got] = true
		}
	}
	var seen []string
	for k := range emitted {
		seen = append(seen, k)
	}
	fatalf(t, reflect.DeepEqual(sorted(seen), sorted(sentinels["projects"])), "project sentinels %v", seen)
}

func TestACompactionWithoutAUUIDKeepsTheDecimalTimestampAsItsID(t *testing.T) {
	plain := rec{"type": "system", "subtype": "compact_boundary", "timestamp": T(5),
		"compactMetadata": rec{"trigger": "manual", "preTokens": 5}}
	state := &extract.State{Pending: map[string]string{}, Calls: map[string]int{}}
	must(t, extract.ApplyRecord(plain, state, 0))
	must(t, extract.ApplyRecord(boundary("uuid-1", 6, "auto", 5), state, 0))
	_, err := strconv.ParseInt(state.Events[0].ID, 10, 64)
	must(t, err)
	equal(t, len(state.Events[1].ID), 16, "hashed id length")
}
