package pricing

import (
	"encoding/binary"
	"math"

	"benchwatch/server/internal/labels"
)

// Long-edge and pixel limits from the model docs (Opus 4.7+, Sonnet 5+); tokens ≈ pixels / 750.
const (
	maxEdge            = 2576
	maxPixels          = 3.75e6
	tokensPerPixel     = 1.0 / 750
	UnknownImageTokens = 1600
	headerB64Chars     = 87384
	// Rough PDF rate: about 2,000 tokens per 60 KB page.
	docBytesPerToken = 30
)

func dimensions(buf []byte) (float64, float64, bool) {
	n := len(buf)
	be16 := func(i int) float64 { return float64(binary.BigEndian.Uint16(buf[i:])) }
	le16 := func(i int) uint32 { return uint32(binary.LittleEndian.Uint16(buf[i:])) }
	le24 := func(i int) float64 { return float64(uint32(buf[i]) | uint32(buf[i+1])<<8 | uint32(buf[i+2])<<16) }
	if n > 24 && binary.BigEndian.Uint32(buf) == 0x89504e47 {
		return float64(binary.BigEndian.Uint32(buf[16:])), float64(binary.BigEndian.Uint32(buf[20:])), true
	}
	if n > 10 && string(buf[:3]) == "GIF" {
		return float64(le16(6)), float64(le16(8)), true
	}
	if n > 30 && string(buf[:4]) == "RIFF" && string(buf[8:12]) == "WEBP" {
		switch string(buf[12:16]) {
		case "VP8X":
			return 1 + le24(24), 1 + le24(27), true
		case "VP8L":
			bits := binary.LittleEndian.Uint32(buf[21:])
			return float64(1 + bits&0x3fff), float64(1 + (bits>>14)&0x3fff), true
		case "VP8 ":
			return float64(le16(26) & 0x3fff), float64(le16(28) & 0x3fff), true
		}
	}
	if n > 4 && buf[0] == 0xff && buf[1] == 0xd8 {
		for i := 2; i+9 < n; {
			if buf[i] != 0xff {
				i++
				continue
			}
			marker := buf[i+1]
			if marker == 0xff {
				i++
				continue
			}
			if marker >= 0xc0 && marker <= 0xcf && marker != 0xc4 && marker != 0xc8 && marker != 0xcc {
				return be16(i + 7), be16(i + 5), true
			}
			i += 2 + int(binary.BigEndian.Uint16(buf[i+2:]))
		}
	}
	return 0, 0, false
}

func ImageTokensFromSize(width, height float64) int64 {
	if !(width > 0 && height > 0) {
		return UnknownImageTokens
	}
	scale := math.Min(1, maxEdge/math.Max(width, height))
	scale = math.Min(scale, math.Sqrt(maxPixels/(width*height)))
	return int64(math.Ceil(width * scale * height * scale * tokensPerPixel))
}

func base64Value(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case c >= '0' && c <= '9':
		return c - '0' + 52, true
	case c == '+' || c == '-':
		return 62, true
	case c == '/' || c == '_':
		return 63, true
	}
	return 0, false
}

// decodeLoose is Node's Buffer.from(text.slice(0, maxUnits), 'base64'): both alphabets, '=' ends the data, other
// bytes are skipped. Node reads only the low byte of each UTF-16 unit, so U+0144 counts as 'D' and U+D83D as '='.
func decodeLoose(text string, maxUnits int) []byte {
	units := make([]byte, 0, min(len(text), maxUnits))
	for _, r := range text {
		if len(units) >= maxUnits {
			break
		}
		if r < 0x10000 {
			units = append(units, byte(r))
			continue
		}
		r -= 0x10000
		units = append(units, byte(0xd800+r>>10))
		if len(units) < maxUnits {
			units = append(units, byte(0xdc00+r&0x3ff))
		}
	}
	out := make([]byte, 0, len(units)*3/4+3)
	var acc uint32
	bits := 0
	for _, c := range units {
		if c == '=' {
			break
		}
		v, ok := base64Value(c)
		if !ok {
			continue
		}
		acc = acc<<6 | uint32(v)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out
}

func ImageTokens(data any) int64 {
	text, ok := data.(string)
	if !ok || text == "" {
		return UnknownImageTokens
	}
	if w, h, found := dimensions(decodeLoose(text, headerB64Chars)); found {
		return ImageTokensFromSize(w, h)
	}
	return UnknownImageTokens
}

func DocumentTokens(data any) int64 {
	text, ok := data.(string)
	if !ok {
		return UnknownImageTokens
	}
	return int64(math.Ceil(float64(labels.UTF16Len(text)) * 0.75 / docBytesPerToken))
}
