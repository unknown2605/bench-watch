package main

import "testing"

func loaded() []hbaRule {
	token := "/" + confinedRole.String()
	return []hbaRule{
		{number: 1, kind: "local", databases: []string{"all"}, users: []string{"all"}, method: "trust"},
		{number: 2, kind: "host", databases: []string{"sameuser"}, users: []string{token}, address: "all",
			method: "scram-sha-256"},
		{number: 3, kind: "host", databases: []string{"all"}, users: []string{token}, address: "all", method: "reject"},
		{number: 4, kind: "host", databases: []string{"all"}, users: []string{"all"}, address: "127.0.0.1",
			method: "trust"},
		{number: 5, kind: "host", databases: []string{"replication"}, users: []string{"all"}, address: "::1",
			method: "trust"},
	}
}

func TestCheckHbaNeedsTheConfiningLinesFirst(t *testing.T) {
	role := "benchwatch_x"
	if err := checkHba(loaded(), role); err != nil {
		t.Fatalf("the stack's rules: %v", err)
	}
	trustAll := hbaRule{kind: "host", databases: []string{"all"}, users: []string{"all"}, address: "all", method: "trust"}
	repl := hbaRule{kind: "host", databases: []string{"replication"}, users: []string{"all"}, address: "all",
		method: "trust"}
	cases := map[string]struct {
		edit func([]hbaRule) []hbaRule
		ok   bool
	}{
		"replication line first": {func(r []hbaRule) []hbaRule { return append([]hbaRule{repl}, r...) }, true},
		"no rules loaded":        {func([]hbaRule) []hbaRule { return nil }, false},
		"reject line missing":    {func(r []hbaRule) []hbaRule { return append(r[:2:2], r[3:]...) }, false},
		"sameuser line missing":  {func(r []hbaRule) []hbaRule { return append(r[:1:1], r[2:]...) }, false},
		"lines swapped":          {func(r []hbaRule) []hbaRule { r[1], r[2] = r[2], r[1]; return r }, false},
		"trust line above":       {func(r []hbaRule) []hbaRule { return append([]hbaRule{trustAll}, r...) }, false},
		"line naming the role above": {func(r []hbaRule) []hbaRule {
			x := trustAll
			x.users = []string{role}
			return append([]hbaRule{x}, r...)
		}, false},
		"regex matching the role above": {func(r []hbaRule) []hbaRule {
			x := trustAll
			x.users = []string{"/^bench"}
			return append([]hbaRule{x}, r...)
		}, false},
		"group line above": {func(r []hbaRule) []hbaRule {
			x := trustAll
			x.users = []string{"+monitors"}
			return append([]hbaRule{x}, r...)
		}, false},
		"regex for others above": {func(r []hbaRule) []hbaRule {
			x := trustAll
			x.users = []string{"/^backup_"}
			return append([]hbaRule{x}, r...)
		}, true},
		"sameuser by password":   {func(r []hbaRule) []hbaRule { r[1].method = "password"; return r }, false},
		"sameuser from one host": {func(r []hbaRule) []hbaRule { r[1].address = "127.0.0.1"; return r }, false},
		"a line with an error":   {func(r []hbaRule) []hbaRule { return append(r, hbaRule{err: true}) }, false},
	}
	for name, c := range cases {
		if err := checkHba(c.edit(loaded()), role); (err == nil) != c.ok {
			t.Errorf("%s: %v, want ok %v", name, err, c.ok)
		}
	}
}
