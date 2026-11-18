package scan

import (
	"bytes"
	"testing"

	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
)

func recordedShard() *Shard {
	return &Shard{Key: "opencode-k", Client: clients.OpenCode, Project: "p", Session: "s", Scope: "main", Offset: 9,
		Lines: 2, Pending: map[string]string{}, Events: []extract.Event{
			{Kind: extract.Call, Ts: 1, ID: "a", Text: "m", N: [5]int64{1, 2, 3, 4, 5}, USD: 0.125, HasUSD: true},
			{Kind: extract.Call, Ts: 1, ID: "b", Text: "m", N: [5]int64{1}, HasUSD: true},
			{Kind: extract.Call, Ts: 1, ID: "c", Text: "m"},
		}}
}

func TestRecordedCostRoundTrips(t *testing.T) {
	s := recordedShard()
	got := decodeShard(encodeShard(s), s.Key)
	if got == nil {
		t.Fatal("refused its own encoding")
	}
	for i, e := range got.Events {
		if e != s.Events[i] {
			t.Errorf("event %d: %+v, want %+v", i, e, s.Events[i])
		}
	}
}

func TestOldOrBadRecordedCostRebuilds(t *testing.T) {
	good := encodeShard(recordedShard())
	for name, data := range map[string][]byte{
		"version 8":    bytes.Replace(good, []byte(`{"v":9,`), []byte(`{"v":8,`), 1),
		"nine fields":  bytes.Replace(good, []byte(`,0.125]`), []byte(`]`), 1),
		"negative usd": bytes.Replace(good, []byte(`,0.125]`), []byte(`,-1]`), 1),
		"text usd":     bytes.Replace(good, []byte(`,0.125]`), []byte(`,"1"]`), 1),
	} {
		if bytes.Equal(data, good) {
			t.Fatalf("%s: the edit changed nothing", name)
		}
		if decodeShard(data, "opencode-k") != nil {
			t.Errorf("%s: decoded, want a rebuild", name)
		}
	}
}
