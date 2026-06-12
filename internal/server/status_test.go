package server

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/home-operations/konflate/internal/api"
)

func TestRenderedStatusDescription(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sig  *api.Signals
		want string
	}{
		{"nil signals", nil, "Rendered the diff"},
		{"singular resource", &api.Signals{Resources: 1}, "1 resource changed"},
		{"plural", &api.Signals{Resources: 3}, "3 resources changed"},
		{
			"resources + caution + failures",
			&api.Signals{Resources: 12, Caution: 2, Failures: 1},
			"12 resources changed, 2 cautions, 1 render failure",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderedStatusDescription(tc.sig); got != tc.want {
				t.Errorf("renderedStatusDescription = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTruncateStatus(t *testing.T) {
	t.Parallel()
	if got := truncateStatus("short"); got != "short" {
		t.Errorf("under-limit input must pass through unchanged, got %q", got)
	}

	got := truncateStatus(strings.Repeat("a", 200))
	if n := utf8.RuneCountInString(got); n != 140 {
		t.Errorf("truncated to %d runes, want the 140-char cap", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated status should end with an ellipsis, got %q", got)
	}

	// A multibyte rune at the boundary must not be split into invalid UTF-8.
	multi := truncateStatus(strings.Repeat("é", 200))
	if !utf8.ValidString(multi) {
		t.Errorf("truncation split a multibyte rune: %q", multi)
	}
}
