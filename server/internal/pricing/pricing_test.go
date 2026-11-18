package pricing

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"testing"

	"benchwatch/server/internal/clients"
)

func TestHaikuLongContext(t *testing.T) {
	usage := func(cacheRead int64) Usage { return Usage{CacheRead: cacheRead, Output: 1e6} }
	if CostOf(clients.ClaudeCode, "claude-haiku-5-5", usage(100000)).Output != 0.5 || CostOf(clients.ClaudeCode, "claude-haiku-5-5", usage(100000)).CacheRead != 0.001 ||
		CostOf(clients.ClaudeCode, "claude-haiku-5-5", usage(100001)).Output != 2.5 || math.Abs(CostOf(clients.ClaudeCode, "claude-haiku-5-5", usage(1e6)).CacheRead-0.05) >= 1e-9 ||
		CostOf(clients.ClaudeCode, "claude-haiku-5-5-20261001", usage(100001)).Output != 2.5 || CostOf(clients.ClaudeCode, "claude-haiku-4-5", usage(1e6)).Output != 5 ||
		CostOf(clients.ClaudeCode, "claude-opus-5-5", usage(1e6)).Output != 20 {
		t.Fatal("Haiku 5.5 is priced by the context above 100k prompt tokens")
	}
	long := Usage{Input: 1e6, CW5: 1e6, CW1h: 1e6}
	if CostOf(clients.ClaudeCode, "claude-haiku-5-5", long).Input != 0.5 || CostOf(clients.ClaudeCode, "claude-haiku-5-5", long).CacheWrite != 0.5*1.25+0.5*2 {
		t.Fatal("long-context rates apply to every part")
	}
	for _, row := range PriceTable() {
		if row.Model == "claude-haiku-5-5" && (row.LongContext == nil || *row.LongContext != (LongContextRow{100000, 0.5, 2.5, 0.05})) {
			t.Fatal("the price table lists the long-context rates")
		}
	}
}

// Node's costs, bit for bit: a fused multiply-add on arm64 would round once where V8 rounds twice.
func TestCostsMatchNodeBitForBit(t *testing.T) {
	data, err := os.ReadFile("testdata/costs.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	n := func(v any) int64 { return int64(v.(float64)) }
	for i, r := range rows {
		c := CostOf(clients.ClaudeCode, r[0].(string), Usage{n(r[1]), n(r[2]), n(r[3]), n(r[4]), n(r[5])})
		total := c.Input + c.CacheWrite + c.CacheRead + c.Output
		rounded := math.Floor(float64(total * 1e6))
		if float64(total*1e6)-rounded >= 0.5 {
			rounded++
		}
		got := []float64{c.Input, c.CacheWrite, c.CacheRead, c.Output, total, rounded / 1e6}
		for j, g := range got {
			if math.Float64bits(g) != math.Float64bits(r[6+j].(float64)) {
				t.Errorf("row %d column %d: go %v node %v", i, j, g, r[6+j])
			}
		}
	}
}

func pngBase64(width, height uint32, pad int) string {
	head := make([]byte, 33+pad)
	binary.BigEndian.PutUint32(head[0:], 0x89504e47)
	binary.BigEndian.PutUint32(head[4:], 0x0d0a1a0a)
	binary.BigEndian.PutUint32(head[8:], 13)
	copy(head[12:], "IHDR")
	binary.BigEndian.PutUint32(head[16:], width)
	binary.BigEndian.PutUint32(head[20:], height)
	return base64.StdEncoding.EncodeToString(head)
}

func TestImagesCostPixels(t *testing.T) {
	b64 := pngBase64(1500, 1000, 2_000_000)
	if ImageTokens(b64) != 2000 || len(b64)/4 <= 300000 {
		t.Fatal("images cost pixels / 750, not base64 length")
	}
	if ImageTokensFromSize(8000, 8000) > 5000 || ImageTokens("not-an-image") != 1600 || ImageTokens(5.0) != 1600 {
		t.Fatal("capped at the model limit, 1600 when unknown")
	}
	urlSafe := base64.URLEncoding.EncodeToString(mustDecode(pngBase64(1500, 1000, 3)))
	if ImageTokens(urlSafe) != 2000 {
		t.Fatal("Node's decoder reads the URL-safe alphabet too")
	}
}

func mustDecode(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// Node's Buffer.from(text, 'base64'), probed on Node 24 and 26: '=' ends the data, other bytes are skipped.
func TestLooseBase64MatchesNode(t *testing.T) {
	cases := map[string]string{
		"QUJD": "414243", "QU JD": "414243", "QUJ$D": "414243", "QU=JD": "41", "QUJDRA": "41424344", "QUJDR": "414243",
		"-_-_": "fbffbf", "+/+/": "fbffbf", "Q": "", "QQ": "41", "=QUJD": "", "QUJ=D": "4142", "ab=c": "69",
		"QUJ" + string(rune(0x144)): "414243", "Q" + string(rune(0x155)) + "JD": "414243",
		"ŁŁŁŁ": "000000", "QU" + string(rune(0x1f600)) + "JD": "41",
	}
	for in, want := range cases {
		got := decodeLoose(in, headerB64Chars)
		if hexOf(got) != want {
			t.Errorf("%q: go %s node %s", in, hexOf(got), want)
		}
	}
	if DocumentTokens("abcd") != 1 || DocumentTokens(nil) != 1600 {
		t.Fatal("documents cost base64 length * 0.75 / 30")
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}
