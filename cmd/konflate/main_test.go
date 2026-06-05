package main

import "testing"

func TestSoftMemLimit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want int64
		ok   bool
	}{
		{"1Gi → 90%", "1073741824\n", 966367641, true},
		{"512Mi, whitespace-trimmed", "  536870912 ", 483183820, true},
		{"no limit", "max", 0, false},
		{"zero", "0", 0, false},
		{"unparsable", "tcp://10.0.0.1:8080", 0, false},
		{"empty", "", 0, false},
	}
	for _, c := range cases {
		got, ok := softMemLimit(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: softMemLimit(%q) = (%d, %v), want (%d, %v)", c.name, c.in, got, ok, c.want, c.ok)
		}
	}
}
