package store

import (
	"slices"
	"testing"

	"benchwatch/server/internal/clients"
)

func TestClientEnumMatchesClients(t *testing.T) {
	var got, want []string
	for _, c := range AllClientIDValues() {
		got = append(got, string(c))
	}
	for _, c := range clients.All() {
		want = append(want, string(c))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("client_id enum %v, clients.All() %v", got, want)
	}
}
