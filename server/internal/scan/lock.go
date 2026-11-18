package scan

import (
	"errors"
	"io/fs"
	"math"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"benchwatch/server/internal/labels"
)

const (
	lockStaleMs = 10 * 60 * 1000
	BeatMs      = 60 * 1000
)

func stamp() []byte {
	return []byte(strconv.Itoa(os.Getpid()) + "\n" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "\n")
}

var decimal = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

// jsNumber is JavaScript's Number(text) for a string.
func jsNumber(text string) float64 {
	s := labels.TrimJS(text)
	switch s {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		if base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]; base != 0 {
			n, ok := new(big.Int).SetString(s[2:], base)
			if !ok {
				return math.NaN()
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f
		}
	}
	if !decimal.MatchString(s) {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseStamp(text string) (pid, at float64) {
	fields := strings.Split(text, "\n")
	pid, at = jsNumber(fields[0]), math.NaN()
	if len(fields) > 1 {
		at = jsNumber(fields[1])
	}
	return pid, at
}

func readStamp(file string) (string, bool, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if isNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

func stampIsLive(text string) bool {
	pid, at := parseStamp(text)
	age := nowMs() - at
	if pid != math.Trunc(pid) || pid <= 0 || pid > math.MaxInt32 || !(age >= 0 && age < lockStaleMs) {
		return false
	}
	err := syscall.Kill(int(pid), 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Of two runs that judged one stamp stale, the slower sees a different stamp in its claim and puts that lock back. A
// third run can link in during that gap; the displaced owner then stops at its next beat, and all writes stay atomic.
func removeIfStill(file, staleText string) (bool, error) {
	claim := tempName(file)
	if err := os.Rename(file, claim); err != nil {
		if isNotExist(err) {
			return true, nil
		}
		return false, err
	}
	defer os.Remove(claim)
	data, err := os.ReadFile(claim)
	if err != nil {
		return false, err
	}
	if string(data) == staleText {
		return true, nil
	}
	if err := os.Link(claim, file); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return false, nil
}

type Lock struct {
	file   string
	beatMs float64
	beatAt float64
}

func NewLock(file string, beatMs float64) *Lock { return &Lock{file: file, beatMs: beatMs} }

// Take publishes the lock with link(), so a reader never sees an empty lock and calls it stale. afterStaleVerdict lets
// a test run a rival takeover between judging a lock stale and removing it.
func (l *Lock) Take(afterStaleVerdict func()) (bool, error) {
	tmp := tempName(l.file)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, err
	}
	_, err = f.Write(stamp())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	defer os.Remove(tmp)
	if err != nil {
		return false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := os.Link(tmp, l.file)
		if err == nil {
			l.beatAt = nowMs()
			return true, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return false, err
		}
		seen, found, err := readStamp(l.file)
		if err != nil {
			return false, err
		}
		if !found {
			continue
		}
		if stampIsLive(seen) {
			return false, nil
		}
		if afterStaleVerdict != nil {
			afterStaleVerdict()
		}
		removed, err := removeIfStill(l.file, seen)
		if err != nil || !removed {
			return false, err
		}
	}
	return false, nil
}

func (l *Lock) Holds() bool {
	data, err := os.ReadFile(l.file)
	if err != nil {
		return false
	}
	pid, _ := parseStamp(string(data))
	return pid == float64(os.Getpid())
}

// Beat keeps a long run live by stamping the lock as it goes; the age check covers a crashed owner whose PID came back.
func (l *Lock) Beat() error {
	if nowMs()-l.beatAt < l.beatMs {
		return nil
	}
	if !l.Holds() {
		return ErrLockLost
	}
	if err := WriteAtomic(l.file, stamp(), 0o600); err != nil {
		return err
	}
	l.beatAt = nowMs()
	return nil
}

func (l *Lock) Release() {
	if l.Holds() {
		_ = os.Remove(l.file)
	}
}
