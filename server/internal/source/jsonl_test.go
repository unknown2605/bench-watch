package source

import (
	"regexp"
	"testing"
)

func TestSessionLabelKeepsAHexHeadAndHashesAnythingElse(t *testing.T) {
	hex8 := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for id, want := range map[string]string{"ABCDEF01-2345": "abcdef01", "0123456789": "01234567"} {
		if got := SessionLabel(id); got != want {
			t.Errorf("%q: %q, want %q", id, got, want)
		}
	}
	for _, id := range []string{"ses_9fZx", "", "my session/../x", "abcdefg1"} {
		if got := SessionLabel(id); !hex8.MatchString(got) || got == SessionLabel(id+"!") {
			t.Errorf("%q: %q is not a stable 8-hex hash", id, got)
		}
	}
}
