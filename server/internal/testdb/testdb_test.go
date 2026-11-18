package testdb

import (
	"errors"
	"testing"
)

func TestOnlyFixtureNamesPass(t *testing.T) {
	for name, want := range map[string]bool{
		"benchwatch_cm00002_test": true, "benchwatch_x1_test": true,
		"benchwatch": false, "benchwatch_test": false, "benchwatch_cm00002_test_nope": false,
		"postgres": false, "benchwatch_CM1_test": false, "": false, "benchwatch_a_b_test": false,
	} {
		if got := IsFixture(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}

func TestCheckRefusesNonFixtureNames(t *testing.T) {
	for name, ok := range map[string]bool{"benchwatch": false, "benchwatch_cm00002_test_nope": false, "postgres": false,
		"": false, "benchwatch_cm00002_test": true} {
		err := Check(func() (string, error) { return name, nil })
		if (err == nil) != ok {
			t.Errorf("%q: err %v, want accepted %v", name, err, ok)
		}
	}
	if Check(func() (string, error) { return "", errors.New("down") }) == nil {
		t.Error("a failed name lookup must refuse")
	}
}
