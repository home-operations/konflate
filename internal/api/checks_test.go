package api

import "testing"

func TestRollup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		passed, failed, pending int
		want                    CheckState
	}{
		{"nothing ran", 0, 0, 0, CheckNone},
		{"all green", 3, 0, 0, CheckSuccess},
		{"a failure wins over passes and pendings", 2, 1, 3, CheckFailure},
		{"pending when none failed", 2, 0, 1, CheckPending},
		{"failure beats pending", 0, 1, 5, CheckFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := Rollup(tc.passed, tc.failed, tc.pending)
			if r.State != tc.want {
				t.Errorf("state = %q, want %q", r.State, tc.want)
			}
			if r.Total != tc.passed+tc.failed+tc.pending || r.Passed != tc.passed || r.Failed != tc.failed {
				t.Errorf("counts wrong: %+v", r)
			}
		})
	}
}
