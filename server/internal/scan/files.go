// Package scan keeps one numeric shard per transcript and resumes each from its last complete line.
package scan

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"
	"time"

	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/source"
)

var (
	ErrNotRegular = source.ErrNotRegular
	ErrTooLarge   = errors.New("EFBIG")
)

// MsFromTimespec is Node's msFromTimeSpec; float64() keeps arm64 from fusing the two steps into one FMA.
func MsFromTimespec(t syscall.Timespec) float64 {
	return float64(float64(t.Sec)*1e3) + float64(t.Nsec)/1e6
}

func tempName(file string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return file + "." + hex.EncodeToString(b) + ".tmp"
}

func WriteTemp(file string, data []byte, mode os.FileMode) (string, error) {
	tmp := tempName(file)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", err
	}
	err = f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func WriteAtomic(file string, data []byte, mode os.FileMode) error {
	tmp, err := WriteTemp(file, data, mode)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// OpenRegular opens without following a link; reader files go through source.Open, which adds the guard.
func OpenRegular(file string) (*os.File, int64, error) { return source.OpenRegular(file) }

func ReadBytes(file string, maxBytes int64) ([]byte, error) {
	f, size, err := OpenRegular(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if size > maxBytes {
		return nil, ErrTooLarge
	}
	buf := make([]byte, size)
	got := 0
	for int64(got) < size {
		n, err := f.ReadAt(buf[got:], int64(got))
		got += n
		if n == 0 || err != nil {
			if err != nil && err != io.EOF {
				return nil, err
			}
			break
		}
	}
	return buf[:got], nil
}

// FileMatches checks a file a container can replace, so it is never parsed and never read past the size we wrote.
func FileMatches(file string, bytes int64, hash string) bool {
	content, err := ReadBytes(file, bytes)
	return err == nil && int64(len(content)) == bytes && extract.HashKey(content) == hash
}

func nowMs() float64 { return float64(time.Now().UnixMilli()) }
