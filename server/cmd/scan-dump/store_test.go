package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"benchwatch/server/internal/aggregate"
	"benchwatch/server/internal/clients"
	"benchwatch/server/internal/extract"
	"benchwatch/server/internal/scan"
)

func TestAKillRightAfterTheIndexIsSavedCannotLeaveTheOldOutputCurrent(t *testing.T) {
	b := sandbox(t)
	mainFile := b.write(mainRel, call("m1", 1, nil))
	b.scan()
	appendTo(t, mainFile, call("m2", 2, nil))
	_, err := scan.UpdateShards(b.root, b.cache, nil)
	must(t, err)
	s := b.scan()
	fatalf(t, !strings.HasPrefix(s.stdout, "no transcript changed"), "stdout %q", s.stdout)
	equal(t, s.data.stat("uniqueCalls"), 2, "calls")
}

func TestTheOutputStateIsDroppedBeforeTheIndexIsWritten(t *testing.T) {
	b := sandbox(t)
	mainFile := b.write(mainRel, call("m1", 1, nil))
	b.scan()
	state := scan.OutputStateFile(b.cache)
	_, err := os.Stat(state)
	must(t, err)
	appendTo(t, mainFile, call("m2", 2, nil))
	oldIndex, _ := os.ReadFile(b.indexFile())
	must(t, os.Remove(b.indexFile()))
	must(t, os.MkdirAll(filepath.Join(b.indexFile(), "blocker"), 0o700))
	_, err = scan.UpdateShards(b.root, b.cache, nil)
	fatalf(t, err != nil, "the index write must fail")
	_, err = os.Stat(state)
	fatalf(t, os.IsNotExist(err), "the output state is gone although the index write failed")
	must(t, os.RemoveAll(b.indexFile()))
	must(t, os.WriteFile(b.indexFile(), oldIndex, 0o600))
	s := b.scan()
	fatalf(t, !strings.HasPrefix(s.stdout, "no transcript changed"), "stdout %q", s.stdout)
	equal(t, s.data.stat("uniqueCalls"), 2, "calls")
}

func TestANoChangeUpdateKeepsTheOutputState(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.scan()
	before, _ := os.ReadFile(scan.OutputStateFile(b.cache))
	_, err := scan.UpdateShards(b.root, b.cache, nil)
	must(t, err)
	after, _ := os.ReadFile(scan.OutputStateFile(b.cache))
	equal(t, string(after), string(before), "output state")
}

func memoryShard(name string, count int) *scan.Shard {
	events := make([]extract.Event, count)
	for i := range events {
		events[i] = extract.Event{Kind: extract.Call, Ts: 1790000000, ID: fmt.Sprintf("%s-%d", name, i), Text: "claude-opus-5-5",
			N: [5]int64{1, 0, 0, 0, 1}}
	}
	return &scan.Shard{Client: clients.ClaudeCode, Project: name, Session: name, Scope: "main", Lines: int64(count), Events: events}
}

func source(shards ...*scan.Shard) aggregate.ShardSource {
	return func(visit func(*scan.Shard) error) error {
		for _, s := range shards {
			if err := visit(s); err != nil {
				return err
			}
		}
		return nil
	}
}

func asData(t *testing.T, d *aggregate.Data) data {
	t.Helper()
	text, err := encode(d)
	must(t, err)
	var out data
	must(t, json.Unmarshal(text, &out))
	return out
}

func callsOf(d data, project string) float64 {
	return total(where(d.rows("usage"), func(r []float64) bool { return d.name("projects", r[1]) == project }), 4)
}

func TestTheRunBudgetKeepsEveryHealthyTranscriptWhole(t *testing.T) {
	inOrder := source(memoryShard("runaway", 1000), memoryShard("a", 10), memoryShard("b", 20), memoryShard("second", 500))
	raw, err := aggregate.Aggregate(inOrder, 130)
	must(t, err)
	d := asData(t, raw)
	for project, want := range map[string]float64{"a": 10, "b": 20, "runaway": 50, "second": 50} {
		equal(t, callsOf(d, project), want, project)
	}
	equal(t, d.stat("itemsDropped"), 1400, "items dropped")
	equal(t, d.stat("uniqueCalls"), 130, "unique calls")
	exact, err := aggregate.Aggregate(inOrder, 1530)
	must(t, err)
	equal(t, exact.Stats.ItemsDropped, 0, "exactly at the budget nothing goes")
	whole, err := aggregate.Aggregate(inOrder, aggregate.MaxRunItems)
	must(t, err)
	equal(t, whole.Stats.ItemsDropped, 0, "default budget")
}

func TestAUsageMergeNeverTakesAValueFromADroppedEvent(t *testing.T) {
	event := func(id string, output int64) extract.Event {
		return extract.Event{Kind: extract.Call, Ts: 1790000000, ID: id, Text: "claude-opus-5-5", N: [5]int64{1, 0, 0, 0, output}}
	}
	shard := func(name string, events ...extract.Event) *scan.Shard {
		return &scan.Shard{Client: clients.ClaudeCode, Project: name, Session: name, Scope: "main", Lines: int64(len(events)), Events: events}
	}
	raw, err := aggregate.Aggregate(source(shard("small", event("late", 7)), shard("big", event("kept", 1), event("late", 100))), 2)
	must(t, err)
	d := asData(t, raw)
	equal(t, d.stat("itemsDropped"), 1, "items dropped")
	equal(t, total(d.rows("usage"), 8), 8, "the dropped event of 100 output tokens does not count")
}

func TestTheBudgetShareIsTheLargestCapThatFits(t *testing.T) {
	equal(t, aggregate.ItemsPerShard([]int{1, 2, 3}, 6), math.MaxInt, "all fit")
	equal(t, aggregate.ItemsPerShard([]int{1, 2, 3}, 5), 2, "cap 2")
	equal(t, aggregate.ItemsPerShard(nil, 5), math.MaxInt, "none")
	equal(t, aggregate.ItemsPerShard([]int{10, 10, 1}, 11), 5, "cap 5")
	equal(t, aggregate.ItemsPerShard([]int{1, 1}, 0), 0, "zero")
	fatalf(t, aggregate.MaxRunItems < 1<<24, "under the V8 limit")
}

func TestAnOutputTooLargeForOneStringStepsTheBudgetDown(t *testing.T) {
	inOrder := source(memoryShard("runaway", 3000), memoryShard("healthy", 5))
	whole, err := aggregate.AggregateToFit(inOrder, 3005, math.MaxInt, outputSize)
	must(t, err)
	full, _ := outputSize(whole)
	fitted, err := aggregate.AggregateToFit(inOrder, 3005, full-1, outputSize)
	must(t, err)
	size, _ := outputSize(fitted)
	fatalf(t, size < full, "not smaller")
	fatalf(t, fitted.Stats.ItemsDropped > 0, "nothing dropped")
	equal(t, callsOf(asData(t, fitted), "healthy"), 5, "healthy calls")
	other := errors.New("not a size problem")
	_, err = aggregate.AggregateToFit(func(func(*scan.Shard) error) error { return other }, aggregate.MaxRunItems, aggregate.MaxOutputChars, outputSize)
	fatalf(t, errors.Is(err, other), "other errors pass through: %v", err)
}

// word is n.toString(26) with its digits spelled as letters, so every tool name passes the label gate.
func word(n int) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return rune("qrstuvwxyz"[r-'0'])
		}
		return r
	}, strconv.FormatInt(int64(n), 26))
}

func TestATableThatWouldGrowPastItsRowLimitStepsTheBudgetDown(t *testing.T) {
	events := make([]extract.Event, 1_000_100)
	for i := range events {
		events[i] = extract.Event{Kind: extract.Result, Ts: 946684800 + int64(i%36500)*86400, ID: fmt.Sprintf("u%d", i),
			Text: "mcp__srv__tool_" + word(i)}
	}
	shard := &scan.Shard{Client: clients.ClaudeCode, Project: "runaway", Session: "s", Scope: "main", Lines: 1, Events: events}
	d, err := aggregate.AggregateToFit(source(shard), aggregate.MaxRunItems, aggregate.MaxOutputChars, outputSize)
	must(t, err)
	fatalf(t, d.Stats.ItemsDropped > 0, "nothing dropped")
	fatalf(t, len(d.Fills) <= 1_000_000, "%d fill rows", len(d.Fills))
}

func TestDroppedItemsReachTheDumpAndTheScanStaysHealthy(t *testing.T) {
	b := sandbox(t)
	useIDs := func(line string) []any {
		out := make([]any, 90_000)
		for i := range out {
			out[i] = rec{"type": "tool_result", "tool_use_id": fmt.Sprintf("%s-%d", line, i), "content": ""}
		}
		return out
	}
	for file := 0; file < 9; file++ {
		var lines []any
		for line := 0; line < 6; line++ {
			lines = append(lines, rec{"type": "user", "timestamp": T(1), "message": rec{"content": useIDs(fmt.Sprintf("%d-%d", file, line))}})
		}
		b.write(fmt.Sprintf("-Users-x-workspace-proj/ccccccc%d-0000-0000-0000-000000000000.jsonl", file), lines...)
	}
	b.write(otherRel, call("o1", 2, nil))
	s := b.scan()
	equal(t, total(s.data.rows("usage"), 4), 1, "the healthy transcript landed")
	fatalf(t, s.data.stat("itemsDropped") > 0, "nothing dropped")
	equal(t, s.status["ok"], any(true), "status")
}

func writeStamp(t *testing.T, file string, pid int, ageMinutes int64) {
	t.Helper()
	must(t, os.WriteFile(file, fmt.Appendf(nil, "%d\n%d\n", pid, time.Now().UnixMilli()-ageMinutes*60*1000), 0o600))
}

func take(t *testing.T, l *scan.Lock, after func()) bool {
	t.Helper()
	ok, err := l.Take(after)
	must(t, err)
	return ok
}

func TestALongRunKeepsItsLockLiveAndALostLockStops(t *testing.T) {
	b := sandbox(t)
	file := filepath.Join(b.dir, "lock")
	first := scan.NewLock(file, 0)
	equal(t, take(t, first, nil), true, "first take")
	writeStamp(t, file, os.Getpid(), 11)
	must(t, first.Beat())
	equal(t, take(t, scan.NewLock(file, scan.BeatMs), nil), false, "stamped again, so another scan sees it as live")
	writeStamp(t, file, os.Getpid(), 11)
	equal(t, take(t, scan.NewLock(file, scan.BeatMs), nil), true, "a lock not stamped for over 10 minutes is taken")

	writeStamp(t, file, os.Getppid(), 0)
	equal(t, take(t, scan.NewLock(file, scan.BeatMs), nil), false, "a live owner keeps it")
	loser := scan.NewLock(file, 0)
	fatalf(t, errors.Is(loser.Beat(), scan.ErrLockLost), "the loser must see ELOCKLOST")
	loser.Release()
	text, _ := os.ReadFile(file)
	fatalf(t, strings.HasPrefix(string(text), fmt.Sprintf("%d\n", os.Getppid())), "the new owner keeps its lock")

	quietFile := filepath.Join(b.dir, "quiet")
	quiet := scan.NewLock(quietFile, scan.BeatMs)
	take(t, quiet, nil)
	stamped, _ := os.ReadFile(quietFile)
	time.Sleep(5 * time.Millisecond)
	must(t, quiet.Beat())
	again, _ := os.ReadFile(quietFile)
	equal(t, string(again), string(stamped), "at most one write per minute")
	quiet.Release()
	_, err := os.Stat(quietFile)
	fatalf(t, os.IsNotExist(err), "released")
}

func TestTheScanTicksThroughFilesAndShardsAndALostLockStopsIt(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.write(otherRel, call("o1", 2, nil))
	ticks := 0
	store, err := scan.UpdateShards(b.root, b.cache, func() error { ticks++; return nil })
	must(t, err)
	fatalf(t, ticks >= 4, "each of two files, plus each read: %d", ticks)
	afterUpdate := ticks
	seen := 0
	must(t, store.Shards(func(*scan.Shard) error { seen++; return nil }))
	equal(t, seen, 2, "shards")
	equal(t, ticks, afterUpdate+2, "one tick per shard")
	_, err = scan.UpdateShards(b.root, b.cache, func() error { return scan.ErrLockLost })
	fatalf(t, errors.Is(err, scan.ErrLockLost), "lost: %v", err)
}

func throwOnTick(nth int) func() error {
	ticks := 0
	return func() error {
		ticks++
		if ticks == nth {
			return scan.ErrLockLost
		}
		return nil
	}
}

func TestALockLostDuringTheReadOfANewTranscriptStopsTheUpdate(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	_, err := scan.UpdateShards(b.root, b.cache, throwOnTick(2))
	fatalf(t, errors.Is(err, scan.ErrLockLost), "got %v", err)
}

func TestALockLostWhileACorruptShardIsRebuiltStopsTheReplay(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	tick := func() error { return nil }
	store, err := scan.UpdateShards(b.root, b.cache, func() error { return tick() })
	must(t, err)
	must(t, os.WriteFile(b.shardFile(mainRel), []byte("junk"), 0o600))
	tick = throwOnTick(2)
	err = store.Shards(func(*scan.Shard) error { return nil })
	fatalf(t, errors.Is(err, scan.ErrLockLost), "got %v", err)
	equal(t, len(store.Failed), 0, "not counted as unrebuilt")
}

func TestEveryLevelAboveTheReadLetsALostLockThrough(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	tick := func() error { return nil }
	store, err := scan.UpdateShards(b.root, b.cache, func() error { return tick() })
	must(t, err)
	must(t, os.WriteFile(b.shardFile(mainRel), []byte("junk"), 0o600))
	tick = throwOnTick(2)
	_, err = aggregate.AggregateToFit(store.Shards, aggregate.MaxRunItems, aggregate.MaxOutputChars, outputSize)
	fatalf(t, errors.Is(err, scan.ErrLockLost), "got %v", err)
	equal(t, codeOf(err), "ELOCKLOST", "code")
}

func TestARunThatLosesItsLockFailsWithELOCKLOSTKeepsTheRivalAndWritesNoDump(t *testing.T) {
	b := sandbox(t)
	b.write(mainRel, call("m1", 1, nil))
	b.write(otherRel, call("o1", 2, nil))
	lockFile := filepath.Join(b.cache, "lock")
	must(t, os.MkdirAll(b.cache, 0o700))
	lock := scan.NewLock(lockFile, 0)
	equal(t, take(t, lock, nil), true, "taken")
	rival := fmt.Sprintf("%d\n%d\n", os.Getppid(), time.Now().UnixMilli())
	must(t, os.WriteFile(lockFile, []byte(rival), 0o600))
	o := b.options()
	o.stdout = io.Discard
	err := dump(o, lock)
	equal(t, codeOf(err), "ELOCKLOST", "code")
	lock.Release()
	_, statErr := os.Stat(b.out)
	fatalf(t, os.IsNotExist(statErr), "no dump")
	text, _ := os.ReadFile(lockFile)
	equal(t, string(text), rival, "the rival keeps its lock")
	must(t, os.Remove(lockFile))
	equal(t, b.scan().data.stat("uniqueCalls"), 2, "the next run, with the rival gone, counts everything")
}

func TestTheSlowerOfTwoTakeoversBacksOff(t *testing.T) {
	b := sandbox(t)
	file := filepath.Join(b.dir, "lock")
	writeStamp(t, file, os.Getppid(), 11)
	stale, _ := os.ReadFile(file)
	winner := scan.NewLock(file, scan.BeatMs)
	slower := scan.NewLock(file, scan.BeatMs)
	var winnerStamp []byte
	got := take(t, slower, func() {
		equal(t, take(t, winner, nil), true, "the faster run takes over inside the window")
		winnerStamp, _ = os.ReadFile(file)
	})
	equal(t, got, false, "two owners would be a double scan")
	fatalf(t, string(winnerStamp) != string(stale), "the winner did not stamp")
	now, _ := os.ReadFile(file)
	equal(t, string(now), string(winnerStamp), "the winner keeps its lock")
	entries, _ := os.ReadDir(b.dir)
	for _, e := range entries {
		fatalf(t, !strings.HasSuffix(e.Name(), ".tmp"), "a claim was left behind")
	}
}

func TestAStaleLockAnotherRunAlreadyRemovedIsTaken(t *testing.T) {
	b := sandbox(t)
	file := filepath.Join(b.dir, "lock")
	writeStamp(t, file, os.Getppid(), 11)
	equal(t, take(t, scan.NewLock(file, scan.BeatMs), func() { _ = os.Remove(file) }), true, "taken")
}
