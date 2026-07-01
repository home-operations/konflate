package api

import "testing"

func TestWarningsByLevel(t *testing.T) {
	t.Parallel()
	ws := []Warning{
		{Level: LevelCaution, Rule: "a"},
		{Level: LevelBlocking, Rule: "b"},
		{Level: LevelCaution, Rule: "c"},
	}
	// Filters to the requested tier, preserving input order.
	if got := WarningsByLevel(ws, LevelCaution); len(got) != 2 || got[0].Rule != "a" || got[1].Rule != "c" {
		t.Errorf("caution filter = %+v, want rules a,c in order", got)
	}
	if got := WarningsByLevel(ws, LevelBlocking); len(got) != 1 || got[0].Rule != "b" {
		t.Errorf("blocking filter = %+v, want rule b", got)
	}
	// Empty and nil inputs yield no matches, not a panic.
	if got := WarningsByLevel(nil, LevelCaution); len(got) != 0 {
		t.Errorf("nil input = %+v, want empty", got)
	}
}
