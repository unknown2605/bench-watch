package scan

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"benchwatch/server/internal/extract"
)

const (
	// SeenEntryBytes is what one remembered result is charged: a 16-byte map key at the map's lowest load after it
	// doubles, rounded up. The memory test holds the real heap under the charge.
	SeenEntryBytes = 48
	// SeenMaxBytes bounds every seen set together. One transcript always fits: it remembers at most one entry per
	// result event, and a shard stops taking events at MaxShardItems, in the middle of a line too.
	SeenMaxBytes = MaxShardItems * SeenEntryBytes
	// seenMaxDepth is how deep a result's content is followed, as extract.MaxToStringDepth; deeper content counts as
	// one fixed value, since the parser has no depth limit and recursion that deep would end the process.
	seenMaxDepth = extract.MaxToStringDepth
	// SeenIdle is how long after its last write a transcript keeps its seen set; it equals the session list's LiveFor.
	SeenIdle = 5 * time.Minute
	// seenTailBytes is how much of a transcript before the offset a set was read to names the place it stopped at.
	seenTailBytes = 256
)

// ErrSeenFull says one transcript alone needs more than the bound, so its repeats cannot be counted and it is not
// read. It cannot happen while the bound holds MaxShardItems entries.
var ErrSeenFull = errors.New("ESEENFULL")

type seenKey [16]byte

// seenSet covers the results of one transcript in bytes [0, offset); tail is the keyed hash of the last of them.
type seenSet struct {
	offset int64
	tail   seenKey
	used   uint64
	hashes map[seenKey]struct{}
}

// seen remembers, for transcripts being written, a keyed hash of each result they returned. It exists in memory only:
// the key is random per process, and nothing here is encoded, logged or returned.
type seen struct {
	mac        hash.Hash
	sets       map[string]*seenSet
	entries    int
	maxEntries int
	clock      uint64
	now        func() time.Time
	evictions  int
	buf        []byte
}

func newSeen() *seen {
	key := make([]byte, 32)
	// crypto/rand.Read never fails: it stops the process when the system has no randomness.
	_, _ = rand.Read(key)
	return &seen{mac: hmac.New(sha256.New, key), sets: map[string]*seenSet{}, maxEntries: SeenMaxBytes / SeenEntryBytes,
		now: time.Now, buf: make([]byte, 32*1024)}
}

func (s *seen) fresh() *seenSet { return &seenSet{hashes: map[seenKey]struct{}{}} }

// tail is a keyed hash of the bytes of f just before offset. Like the result hashes it never leaves memory.
func (s *seen) tail(f *os.File, offset int64) (seenKey, error) {
	var key seenKey
	length := min(seenTailBytes, offset)
	if length == 0 {
		return key, nil
	}
	buf := s.buf[:length]
	got, err := f.ReadAt(buf, offset-length)
	if err != nil && err != io.EOF {
		return key, err
	}
	s.mac.Reset()
	s.tag('t', uint64(got))
	s.mac.Write(buf[:got])
	copy(key[:], s.mac.Sum(nil))
	return key, nil
}

// take hands over the set of key when it covers exactly the bytes a shard was read to and f still holds its tail
// there; any other set is dropped.
func (s *seen) take(f *os.File, key string, offset int64) (*seenSet, error) {
	set := s.sets[key]
	if set == nil {
		return nil, nil
	}
	delete(s.sets, key)
	if set.offset == offset {
		tail, err := s.tail(f, offset)
		if err == nil && tail == set.tail {
			return set, nil
		}
		s.drop(set)
		return nil, err
	}
	s.drop(set)
	return nil, nil
}

func (s *seen) forget(key string) {
	if set := s.sets[key]; set != nil {
		delete(s.sets, key)
		s.drop(set)
	}
}

func (s *seen) drop(set *seenSet) {
	s.entries -= len(set.hashes)
	set.hashes = nil
}

// keep stores a set taken or built for key once its shard is committed; a transcript that is not live keeps none.
func (s *seen) keep(key string, set *seenSet, mtimeMs float64) {
	s.forget(key)
	if !s.live(mtimeMs) {
		s.drop(set)
		return
	}
	s.clock++
	set.used = s.clock
	s.sets[key] = set
}

func (s *seen) live(mtimeMs float64) bool {
	return float64(s.now().UnixMilli())-mtimeMs < float64(SeenIdle.Milliseconds())
}

// expire drops the sets of transcripts that are gone or went idle; mtimeMs reports a transcript's last write.
func (s *seen) expire(mtimeMs func(key string) (float64, bool)) {
	for key := range s.sets {
		if at, ok := mtimeMs(key); !ok || !s.live(at) {
			s.forget(key)
		}
	}
}

// evict drops the least recently read stored set; the transcript it belonged to is reread when it grows again.
func (s *seen) evict() bool {
	oldest := ""
	for key, set := range s.sets {
		if oldest == "" || set.used < s.sets[oldest].used {
			oldest = key
		}
	}
	if oldest == "" {
		return false
	}
	s.forget(oldest)
	s.evictions++
	return true
}

// repeat reports whether set already holds this result of the tool labelled label, and remembers it.
func (s *seen) repeat(set *seenSet, label string, content any) (bool, error) {
	s.mac.Reset()
	s.text(label)
	s.value(content, 0)
	var key seenKey
	copy(key[:], s.mac.Sum(s.buf[:0]))
	if _, ok := set.hashes[key]; ok {
		return true, nil
	}
	for s.entries >= s.maxEntries {
		if !s.evict() {
			return false, ErrSeenFull
		}
	}
	set.hashes[key] = struct{}{}
	s.entries++
	return false, nil
}

func (s *seen) tag(kind byte, n uint64) {
	var head [9]byte
	head[0] = kind
	binary.LittleEndian.PutUint64(head[1:], n)
	s.mac.Write(head[:])
}

func (s *seen) text(v string) {
	s.tag('s', uint64(len(v)))
	for len(v) > 0 {
		n := copy(s.buf, v)
		s.mac.Write(s.buf[:n])
		v = v[n:]
	}
}

// value feeds one parsed JSON value in a form that does not depend on map order.
func (s *seen) value(v any, depth int) {
	switch x := v.(type) {
	case string:
		s.text(x)
	case float64:
		s.tag('n', math.Float64bits(x))
	case bool:
		if x {
			s.tag('b', 1)
		} else {
			s.tag('b', 0)
		}
	case []any:
		if depth >= seenMaxDepth {
			s.tag('d', 0)
			return
		}
		s.tag('a', uint64(len(x)))
		for _, item := range x {
			s.value(item, depth+1)
		}
	case map[string]any:
		if depth >= seenMaxDepth {
			s.tag('d', 0)
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		s.tag('o', uint64(len(keys)))
		for _, k := range keys {
			s.text(k)
			s.value(x[k], depth+1)
		}
	default:
		s.tag('z', 0)
	}
}
