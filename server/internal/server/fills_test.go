package server

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"benchwatch/server/internal/guardlog"
	"benchwatch/server/internal/store"
	"benchwatch/server/internal/transcripts"
)

func TestFillsByProjectKeepsEveryTool(t *testing.T) {
	rows := []store.ToolByProjectRow{}
	for _, tool := range []string{"Bash: git", "Bash: ls", "Bash: npm", "Bash: go", "Read: whole"} {
		rows = append(rows, store.ToolByProjectRow{Project: "a", Tool: tool, WeightedTokens: 10})
	}
	rows = append(rows, store.ToolByProjectRow{Project: "b", Tool: "Read: whole", WeightedTokens: 7})
	got := fillsByProject(rows)
	if len(got) != 2 || got[0].Project != "a" || got[0].WeightedTokens != 50 || len(got[0].Tools) != 5 {
		t.Fatalf("project a lost tools: %+v", got)
	}
	if got[1].Project != "b" || len(got[1].Tools) != 1 || got[1].Tools[0].WeightedTokens != 7 {
		t.Fatalf("project b: %+v", got[1])
	}
}

func TestSizeBandGroupsCarryWeightedTokens(t *testing.T) {
	top := int64(1000)
	sizes := []store.ToolSizeBandsRow{
		{Tool: "Read", MinTokens: 0, MaxTokens: &top, Results: 3, Tokens: 100},
		{Tool: "Read", MinTokens: 1000, Results: 1, Tokens: 300},
		{Tool: "Grep", MinTokens: 0, MaxTokens: &top, Results: 2, Tokens: 50},
	}
	stats := []store.ToolStatsRow{
		{Tool: "Read", Results: 4, Tokens: 400, WeightedTokens: 4000},
		{Tool: "Grep", Results: 2, Tokens: 50, WeightedTokens: 500},
	}
	got := sizeBandGroups(sizes, stats)
	if len(got) != 2 || got[0].WeightedTokens != 1500 || got[1].WeightedTokens != 3000 {
		t.Fatalf("weighted tokens per band: %+v", got)
	}
	if got[0].Results != 5 || got[0].Tokens != 150 {
		t.Fatalf("band sums: %+v", got[0])
	}
}

func TestEveryCollectorCodeFitsTheAPIPattern(t *testing.T) {
	codes := []string{"ELOCKED", "ELOCKLOST", "ESEENFULL", "ETOOMANYROWS", "ENOSINK", "EPARTIAL", "ETIMEDOUT", "ECANCELED",
		"ERRNO", "EOTHER", guardlog.ErrNotRegular.Error(), guardlog.ErrState.Error(), "EPG_23505", "EPG_CONNECT", "",
		"lower", "E", "ETOOLONGCODEFORTHEPATTERNANDTHENSOMEMORE"}
	for _, e := range []syscall.Errno{syscall.ENOENT, syscall.EACCES, syscall.EPERM, syscall.ENOSPC, syscall.ELOOP,
		syscall.ENOTDIR, syscall.EISDIR, syscall.EMFILE, syscall.ENFILE, syscall.EIO, syscall.EROFS, syscall.EBUSY,
		syscall.EDQUOT, syscall.ENAMETOOLONG, syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.ETIMEDOUT, syscall.EPIPE,
		syscall.EAGAIN} {
		codes = append(codes, transcripts.Code(fmt.Errorf("read: %w", e)))
	}
	codes = append(codes, transcripts.Code(&pgconn.PgError{Code: "40001"}), transcripts.Code(errors.New("x")),
		transcripts.Code(&pgconn.ConnectError{}))
	for _, c := range codes {
		if got := guardCode(c); !guardErrorCode.MatchString(got) {
			t.Errorf("%q becomes %q, which breaks the pattern", c, got)
		}
	}
	for in, want := range map[string]string{"EACCES": "EACCES", "EPG_23505": "EPG_23505", "": "EUNKNOWN", "bad": "EUNKNOWN"} {
		if got := guardCode(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
