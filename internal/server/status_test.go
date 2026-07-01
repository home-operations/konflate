package server

import (
	"strings"
	"testing"
	"unicode"
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
		{
			"blocking ranks before caution",
			&api.Signals{Resources: 5, Blocking: 1, Caution: 2},
			"5 resources changed, 1 blocker, 2 cautions",
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

func TestOneLine(t *testing.T) {
	t.Parallel()
	// oneLine guards the commit-status description against newline / control-char
	// injection from a fork-PR render error.
	cases := []struct{ name, in, want string }{
		{"newline to space", "line1\nline2", "line1 line2"},
		{"crlf and tab collapse", "a\r\n\tb", "a b"},
		{"esc and nul become spaces", "a\x1b\x00b", "a b"},
		{"runs collapse and trim", "  multiple   spaces  ", "multiple spaces"},
		{"plain passes through", "plain", "plain"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := oneLine(tc.in)
			if got != tc.want {
				t.Errorf("oneLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, r := range got {
				if unicode.IsControl(r) {
					t.Errorf("oneLine(%q) left a control char %U", tc.in, r)
				}
			}
		})
	}
}
