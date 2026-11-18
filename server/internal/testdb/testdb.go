// Package testdb is the one gate every database test passes before it deletes anything.
package testdb

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
)

// fixtureName is the shape of the throwaway databases bin/test-db.sh creates (benchwatch_cm00002_test).
var fixtureName = regexp.MustCompile(`^benchwatch_[a-z0-9]+_test$`)

// DSN returns CM_TEST_DSN after proving it points at a fixture database; the test is skipped when it is unset and
// fails when it names any other database.
func DSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("CM_TEST_DSN")
	if dsn == "" {
		t.Skip("CM_TEST_DSN not set (run bin/test-db.sh)")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	err = Check(func() (string, error) {
		var name string
		return name, conn.QueryRow(ctx, "SELECT current_database()").Scan(&name)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

// IsFixture reports whether name is a database the fixture script makes.
func IsFixture(name string) bool { return fixtureName.MatchString(name) }

// Check refuses unless the database name from current is a fixture database.
func Check(current func() (string, error)) error {
	name, err := current()
	if err != nil {
		return fmt.Errorf("current_database: %w", err)
	}
	if !IsFixture(name) {
		return fmt.Errorf("CM_TEST_DSN points at database %q, not a fixture database; refusing to run tests that delete rows", name)
	}
	return nil
}
