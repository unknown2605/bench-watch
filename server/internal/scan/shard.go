package scan

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
)

// Version 8 (CM-00016) added the client and prefixed every key with it; version 9 added a call's recorded USD.
// Each bump makes every stored shard fail decoding, so every unit is read once more.
const ShardVersion = 9

type Shard struct {
	Key       string
	Client    clients.ID
	Project   string
	Session   string
	Scope     string
	AgentType *string
	Offset    int64
	Size      int64
	MtimeMs   float64
	LastTs    int64
	Skipped   int64
	Lines     int64
	Pending   map[string]string
	Events    []extract.Event
}

type Entry struct {
	Size       int64
	MtimeMs    float64
	BirthMs    float64
	FirstTs    float64
	Sub        bool
	ShardBytes int64
	ShardHash  string
}

func appendString(b []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			b = utf8.AppendRune(b, r)
			i += size
			continue
		}
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&15])
		default:
			b = append(b, c)
		}
		i++
	}
	return append(b, '"')
}

func appendFloat(b []byte, f float64) []byte { return strconv.AppendFloat(b, f, 'g', -1, 64) }

func appendInt(b []byte, n int64) []byte { return strconv.AppendInt(b, n, 10) }

func appendField(b []byte, name string) []byte {
	b = append(b, ',')
	b = appendString(b, name)
	return append(b, ':')
}

func encodeShard(s *Shard) []byte {
	b := make([]byte, 0, 256+len(s.Events)*80)
	b = append(b, `{"v":`...)
	b = appendInt(b, ShardVersion)
	for _, f := range [...]struct{ name, value string }{{"key", s.Key}, {"client", string(s.Client)}, {"project", s.Project}, {"session", s.Session}, {"scope", s.Scope}} {
		b = appendField(b, f.name)
		b = appendString(b, f.value)
	}
	b = appendField(b, "agentType")
	if s.AgentType == nil {
		b = append(b, "null"...)
	} else {
		b = appendString(b, *s.AgentType)
	}
	b = appendField(b, "offset")
	b = appendInt(b, s.Offset)
	b = appendField(b, "size")
	b = appendInt(b, s.Size)
	b = appendField(b, "mtimeMs")
	b = appendFloat(b, s.MtimeMs)
	b = appendField(b, "lastTs")
	b = appendInt(b, s.LastTs)
	b = appendField(b, "skipped")
	b = appendInt(b, s.Skipped)
	b = appendField(b, "lines")
	b = appendInt(b, s.Lines)
	b = appendField(b, "pending")
	b = append(b, '[')
	keys := make([]string, 0, len(s.Pending))
	for k := range s.Pending {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '[')
		b = appendString(b, k)
		b = append(b, ',')
		b = appendString(b, s.Pending[k])
		b = append(b, ']')
	}
	b = append(b, ']')
	b = appendField(b, "events")
	b = append(b, '[')
	for i := range s.Events {
		e := &s.Events[i]
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '[')
		b = appendInt(b, int64(e.Kind))
		b = append(b, ',')
		b = appendInt(b, e.Ts)
		b = append(b, ',')
		b = appendString(b, e.ID)
		b = append(b, ',')
		b = appendString(b, e.Text)
		count := 1
		if e.Kind == extract.Call {
			count = 5
		}
		for _, n := range e.N[:count] {
			b = append(b, ',')
			b = appendInt(b, n)
		}
		if e.Kind == extract.Call {
			if e.HasUSD {
				b = append(b, ',')
				b = appendFloat(b, e.USD)
			} else {
				b = append(b, ",null"...)
			}
		}
		if e.Kind == extract.Result {
			b = append(b, ',')
			if e.Error == "" {
				b = append(b, '0')
			} else {
				b = appendString(b, e.Error)
			}
			if e.Repeat {
				b = append(b, ",1"...)
			} else {
				b = append(b, ",0"...)
			}
		}
		b = append(b, ']')
	}
	return append(b, "]}"...)
}

func isCount(v any) bool {
	f, ok := v.(float64)
	return ok && f >= 0 && f <= 9007199254740991 && f == math.Trunc(f)
}

func isTime(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsInf(f, 0) && f == f
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func validEvent(v any) (extract.Event, bool) {
	e, ok := v.([]any)
	if !ok || len(e) < 5 || !isTime(e[1]) || !isString(e[2]) || !isString(e[3]) || !isCount(e[1]) {
		return extract.Event{}, false
	}
	event := extract.Event{Ts: int64(e[1].(float64)), ID: e[2].(string), Text: e[3].(string)}
	switch e[0] {
	case float64(extract.Call):
		if len(e) != 10 {
			return event, false
		}
		event.Kind = extract.Call
		for i := range 5 {
			if !isCount(e[4+i]) {
				return event, false
			}
			event.N[i] = int64(e[4+i].(float64))
		}
		if e[9] != nil {
			usd, ok := e[9].(float64)
			if !ok || math.IsInf(usd, 0) || usd != usd || usd < 0 {
				return event, false
			}
			event.USD, event.HasUSD = usd, true
		}
	case float64(extract.Result):
		if len(e) != 7 || !isCount(e[4]) || (e[6] != float64(0) && e[6] != float64(1)) {
			return event, false
		}
		event.Repeat = e[6] == float64(1)
		event.Kind = extract.Result
		event.N[0] = int64(e[4].(float64))
		switch errValue := e[5].(type) {
		case string:
			event.Error = errValue
		case float64:
			if errValue != 0 {
				return event, false
			}
		default:
			return event, false
		}
	case float64(extract.Boundary):
		if len(e) != 5 || !isCount(e[4]) {
			return event, false
		}
		event.Kind = extract.Boundary
		event.N[0] = int64(e[4].(float64))
	default:
		return event, false
	}
	return event, true
}

// decodeShard validates one shard file; this cache only ever writes whole-second timestamps, so others fail too.
func decodeShard(data []byte, key string) *Shard {
	parsed, err := extract.Parse(data)
	if err != nil {
		return nil
	}
	get := func(name string) any { return extract.Get(parsed, name) }
	scope := get("scope")
	agent := get("agentType")
	clientName, _ := get("client").(string)
	client, clientErr := clients.Parse(clientName)
	if clientErr != nil || !strings.HasPrefix(key, string(client)+"-") {
		return nil
	}
	if get("v") != float64(ShardVersion) || get("key") != key || !isString(get("project")) || !isString(get("session")) ||
		(scope != "main" && scope != "sub") || (agent != nil && !isString(agent)) ||
		!isCount(get("offset")) || !isCount(get("size")) || !isTime(get("mtimeMs")) ||
		!isCount(get("lastTs")) || !isCount(get("skipped")) || !isCount(get("lines")) {
		return nil
	}
	pending, ok1 := get("pending").([]any)
	events, ok2 := get("events").([]any)
	if !ok1 || !ok2 {
		return nil
	}
	s := &Shard{
		Key: key, Client: client, Project: get("project").(string), Session: get("session").(string), Scope: scope.(string),
		Offset: int64(get("offset").(float64)), Size: int64(get("size").(float64)),
		MtimeMs: get("mtimeMs").(float64), LastTs: int64(get("lastTs").(float64)),
		Skipped: int64(get("skipped").(float64)), Lines: int64(get("lines").(float64)),
		Pending: make(map[string]string, len(pending)), Events: make([]extract.Event, 0, len(events)),
	}
	if text, ok := agent.(string); ok {
		s.AgentType = &text
	}
	if s.Offset == 0 && (s.Lines != 0 || len(events) != 0) {
		return nil
	}
	for _, p := range pending {
		pair, ok := p.([]any)
		if !ok || len(pair) != 2 || !isString(pair[0]) || !isString(pair[1]) {
			return nil
		}
		s.Pending[pair[0].(string)] = pair[1].(string)
	}
	for _, v := range events {
		event, ok := validEvent(v)
		if !ok {
			return nil
		}
		s.Events = append(s.Events, event)
	}
	return s
}

func encodeIndex(keys []string, entries map[string]Entry) []byte {
	b := make([]byte, 0, 64+len(keys)*200)
	b = append(b, `{"v":`...)
	b = appendInt(b, ShardVersion)
	b = append(b, `,"files":{`...)
	for i, k := range keys {
		e := entries[k]
		if i > 0 {
			b = append(b, ',')
		}
		b = appendString(b, k)
		b = append(b, `:{"size":`...)
		b = appendInt(b, e.Size)
		b = appendField(b, "mtimeMs")
		b = appendFloat(b, e.MtimeMs)
		b = appendField(b, "birthMs")
		b = appendFloat(b, e.BirthMs)
		b = appendField(b, "firstTs")
		b = appendFloat(b, e.FirstTs)
		b = appendField(b, "sub")
		b = strconv.AppendBool(b, e.Sub)
		b = appendField(b, "shardBytes")
		b = appendInt(b, e.ShardBytes)
		b = appendField(b, "shardHash")
		b = appendString(b, e.ShardHash)
		b = append(b, '}')
	}
	return append(b, "}}"...)
}

func decodeIndex(data []byte) map[string]Entry {
	out := map[string]Entry{}
	parsed, err := extract.Parse(data)
	if err != nil || extract.Get(parsed, "v") != float64(ShardVersion) {
		return out
	}
	files, ok := extract.Get(parsed, "files").(map[string]any)
	if !ok {
		return out
	}
	for key, v := range files {
		get := func(name string) any { return extract.Get(v, name) }
		sub, isBool := get("sub").(bool)
		if !isCount(get("size")) || !isTime(get("mtimeMs")) || !isTime(get("birthMs")) || !isTime(get("firstTs")) ||
			!isBool || !isCount(get("shardBytes")) || !isString(get("shardHash")) {
			continue
		}
		out[key] = Entry{
			Size: int64(get("size").(float64)), MtimeMs: get("mtimeMs").(float64), BirthMs: get("birthMs").(float64),
			FirstTs: get("firstTs").(float64), Sub: sub, ShardBytes: int64(get("shardBytes").(float64)),
			ShardHash: get("shardHash").(string),
		}
	}
	return out
}
