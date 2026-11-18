package machine

import (
	"bufio"
	"bytes"
	"regexp"
	"strconv"

	"benchwatch/server/internal/collect"
)

var (
	ioregAnyNode = regexp.MustCompile(`^[\s|]*\+-o `)
	ioregCPUNode = regexp.MustCompile(`^[\s|]*\+-o cpu\d+@`)
	ioregCluster = regexp.MustCompile(`"cluster-type" = <"([EP])">`)
	ioregLogical = regexp.MustCompile(`"logical-cpu-id" = (\d+)`)
)

// ParseIOReg indexes by logical-cpu-id, as host_processor_info does (on an M2 Pro cpu-id skips 7); every index
// below ncpu must be typed exactly once, and properties count only inside a cpuN node.
func ParseIOReg(out []byte, ncpu int) ([]collect.CoreKind, bool) {
	if ncpu <= 0 {
		return nil, false
	}
	kinds := make([]collect.CoreKind, ncpu)
	seen := 0
	inCPU := false
	var kind collect.CoreKind
	id := -1
	flush := func() bool {
		defer func() { kind, id = "", -1 }()
		if !inCPU {
			return true
		}
		if kind == "" || id < 0 || id >= ncpu || kinds[id] != "" {
			return false
		}
		kinds[id] = kind
		seen++
		return true
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if ioregAnyNode.Match(line) {
			if !flush() {
				return nil, false
			}
			inCPU = ioregCPUNode.Match(line)
			continue
		}
		if !inCPU {
			continue
		}
		if m := ioregCluster.FindSubmatch(line); m != nil {
			kind = collect.CoreKind(m[1])
		} else if m := ioregLogical.FindSubmatch(line); m != nil {
			n, err := strconv.Atoi(string(m[1]))
			if err != nil {
				return nil, false
			}
			id = n
		}
	}
	if !flush() || seen != ncpu {
		return nil, false
	}
	return kinds, true
}

func countKind(kinds []collect.CoreKind, k collect.CoreKind) int {
	n := 0
	for _, v := range kinds {
		if v == k {
			n++
		}
	}
	return n
}

func unknownKinds(n int) []collect.CoreKind {
	k := make([]collect.CoreKind, n)
	for i := range k {
		k[i] = collect.CoreUnknown
	}
	return k
}
