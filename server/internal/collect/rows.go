package collect

import (
	"cmp"
	"slices"
)

// MaxRows bounds every named list in a sample, the Other row included, so a host with thousands of
// containers or interfaces cannot grow one sample without limit.
const MaxRows = 64

// CapRows returns rows sorted by name with at most MaxRows of them: past that, the rows that rank last
// (rank sorts the most important first) are merged into the one Other row, created from other when absent.
func CapRows[T any](rows []T, name func(T) string, rank func(a, b T) int, other T, merge func(o *T, r T)) []T {
	byName := func(a, b T) int { return cmp.Compare(name(a), name(b)) }
	if len(rows) <= MaxRows {
		slices.SortFunc(rows, byName)
		return rows
	}
	named := make([]T, 0, len(rows))
	for _, r := range rows {
		if name(r) == Other {
			other = r
		} else {
			named = append(named, r)
		}
	}
	slices.SortStableFunc(named, rank)
	cut := min(len(named), MaxRows-1)
	for _, r := range named[cut:] {
		merge(&other, r)
	}
	out := append(named[:cut:cut], other)
	slices.SortFunc(out, byName)
	return out
}
