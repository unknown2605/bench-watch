package source

import (
	"errors"
	"maps"
	"math"
	"testing"

	"benchwatch/server/internal/extract"
)

func TestAddRecordedKeepsTheLargerCost(t *testing.T) {
	c := NewCalls(Cursor{}, 0)
	found := Tokens{Found: true}
	c.AddRecorded("a", 1, "m", [5]int64{1}, found, 0.5)
	c.AddRecorded("a", 1, "m", [5]int64{2}, found, 0.25)
	c.Add("a", 1, "m", [5]int64{1}, found)
	c.Add("b", 1, "m", [5]int64{1}, found)
	c.AddRecorded("b", 1, "m", [5]int64{1}, found, 0)
	c.AddRecorded("n", 1, "m", [5]int64{1}, found, math.NaN())
	e := c.Cur.Events
	if len(e) != 3 || e[0].USD != 0.5 || !e[0].HasUSD || e[0].N[0] != 2 {
		t.Fatalf("a: %+v", e)
	}
	if !e[1].HasUSD || e[1].USD != 0 {
		t.Errorf("b: a later recorded 0 is still a cost: %+v", e[1])
	}
	if e[2].HasUSD {
		t.Errorf("n: NaN kept as a cost: %+v", e[2])
	}
}

func TestAddRecordedSkipsACallWhoseCostTheDatabaseCannotHold(t *testing.T) {
	c := NewCalls(Cursor{}, 0)
	c.AddRecorded("big", 1, "m", [5]int64{1}, Tokens{Found: true}, 1e15)
	c.AddRecorded("cap", 1, "m", [5]int64{1}, Tokens{Found: true}, MaxRecordedUSD)
	c.AddRecorded("cap", 1, "m", [5]int64{9}, Tokens{Found: true}, 1e15)
	e := c.Cur.Events
	if len(e) != 1 || c.Cur.Skipped != 2 {
		t.Fatalf("a 1e15 USD call was stored or not counted: %+v skipped %d", e, c.Cur.Skipped)
	}
	if e[0].ID != "cap" || !e[0].HasUSD || e[0].USD != MaxRecordedUSD || e[0].N[0] != 1 {
		t.Errorf("a cost at the cap was dropped, or a skipped repeat raised it: %+v", e[0])
	}
}

func TestAddSkipsAUsageRecordWithoutANumericTokenField(t *testing.T) {
	c := NewCalls(Cursor{}, 0)
	var none, zero Tokens
	none.Count("12")
	none.Count(nil)
	zero.Count(0.0)
	c.Add("renamed", 1, "m", [5]int64{}, none)
	c.AddRecorded("renamed2", 1, "m", [5]int64{}, none, 0.1)
	c.Add("zero", 1, "m", [5]int64{}, zero)
	if len(c.Cur.Events) != 1 || c.Cur.Events[0].ID != "zero" || c.Cur.Skipped != 2 {
		t.Fatalf("events %+v skipped %d", c.Cur.Events, c.Cur.Skipped)
	}
}

func unrecognised(unit Unit, cur Cursor, unknown bool) error {
	return Unrecognised(unit, Cursor{}, &cur, unknown)
}

func TestUnrecognisedFlagsALargeUnitWithoutUsageOrAnyUnitWithoutAKnownRecord(t *testing.T) {
	big, small := Unit{Size: FormatFloor}, Unit{Size: FormatFloor - 1}
	known := Cursor{Lines: 3}
	known.MarkKnown()
	if !errors.Is(unrecognised(big, Cursor{Lines: 3}, false), ErrUnrecognised) ||
		!errors.Is(unrecognised(big, known, false), ErrUnrecognised) {
		t.Error("a large unit with lines and no usage passed")
	}
	if !errors.Is(unrecognised(small, Cursor{Lines: 3}, true), ErrUnrecognised) {
		t.Error("a small unit whose lines are all of unknown types passed")
	}
	if unrecognised(small, known, true) != nil || unrecognised(small, Cursor{Lines: 1}, false) != nil ||
		unrecognised(big, Cursor{}, true) != nil ||
		unrecognised(small, Cursor{Lines: 3, Events: make([]extract.Event, 1)}, true) != nil {
		t.Error("a small known session, a header alone, an empty or a used unit was flagged")
	}
}

func TestUnrecognisedDoesNotFlagALargeUnitWhoseOnlyUsageWasWithheld(t *testing.T) {
	cur := Cursor{Lines: 3, Skipped: 1}
	cur.MarkKnown()
	cur.MarkWithheld()
	if err := unrecognised(Unit{Size: FormatFloor}, cur, false); err != nil {
		t.Fatalf("withheld usage flagged: %v", err)
	}
}

// A resumed cursor from a build before PendingKnown cannot say whether a known record came, so it is not flagged;
// one this build wrote after a header alone records "0" and is.
func TestUnrecognisedDoesNotFlagALegacyResumedCursor(t *testing.T) {
	small := Unit{Size: FormatFloor - 1}
	legacy := Cursor{Lines: 1, Offset: 10}
	cur := Cursor{Lines: 3, Offset: 30}
	if err := Unrecognised(small, legacy, &cur, true); err != nil {
		t.Fatalf("legacy resumed cursor flagged: %v", err)
	}
	if _, ok := cur.Pending[PendingKnown]; ok {
		t.Errorf("legacy cursor got a known key %q, which would make it look recorded", cur.Pending[PendingKnown])
	}
	header := Cursor{Lines: 1, Offset: 10}
	if err := Unrecognised(small, Cursor{}, &header, false); err != nil || header.Pending[PendingKnown] != "0" {
		t.Fatalf("header alone: %v, key %q", err, header.Pending[PendingKnown])
	}
	next := Cursor{Lines: 3, Offset: 30, Pending: maps.Clone(header.Pending)}
	if err := Unrecognised(small, header, &next, true); !errors.Is(err, ErrUnrecognised) {
		t.Errorf("unknown lines after a recorded header passed: %v", err)
	}
}
