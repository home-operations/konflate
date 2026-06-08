package prfilter

import (
	"testing"
	"time"
)

func TestCompile_Invalid(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, expr string }{
		{"syntax error", "pr.draft &&"},
		{"unknown variable", "foo == 1"},
		{"unknown function", "frobnicate(pr)"},
		{"int literal not bool", "42"},
		{"string literal not bool", `"main"`},
		{"arithmetic not bool", "pr.number + 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Compile(c.expr); err == nil {
				t.Fatalf("Compile(%q) = nil error, want error", c.expr)
			}
		})
	}
}

func TestCompile_Valid(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"!pr.draft",
		`pr.author == "renovate[bot]"`,
		`pr.baseRef == "main" && !pr.fork`,
		`pr.labels.exists(l, l.name == "cluster/production")`,
		`"area/storage" in pr.labels.map(l, l.name)`,
		`pr.title.startsWith("feat")`,
		"pr.draft", // statically dyn under the map model — Eval enforces bool
	} {
		if _, err := Compile(expr); err != nil {
			t.Fatalf("Compile(%q): unexpected error: %v", expr, err)
		}
	}
}

// sampleVars is a realistic pr field map; over replaces individual fields.
func sampleVars(over map[string]any) map[string]any {
	pr := map[string]any{
		"number": 142, "title": "feat(rook): bump operator", "author": "renovate[bot]",
		"state": "open", "open": true, "merged": false, "draft": false, "fork": false,
		"headRef": "renovate/rook", "headSha": "abc1234", "baseRef": "main", "url": "https://x/142",
		"createdAt": time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		"labels": []any{
			map[string]any{"name": "area/storage", "color": "0e8a16"},
			map[string]any{"name": "cluster/production", "color": "d93f0b"},
		},
	}
	for k, v := range over {
		pr[k] = v
	}
	return pr
}

func TestEval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, expr string
		pr         map[string]any
		want       bool
	}{
		{"label present", `pr.labels.exists(l, l.name == "cluster/production")`, sampleVars(nil), true},
		{"label absent", `pr.labels.exists(l, l.name == "cluster/staging")`, sampleVars(nil), false},
		{"not draft", "!pr.draft", sampleVars(nil), true},
		{"is draft", "!pr.draft", sampleVars(map[string]any{"draft": true}), false},
		{"author and base", `pr.author == "renovate[bot]" && pr.baseRef == "main"`, sampleVars(nil), true},
		{"exclude forks", "!pr.fork", sampleVars(map[string]any{"fork": true}), false},
		{"label name in list", `"area/storage" in pr.labels.map(l, l.name)`, sampleVars(nil), true},
		{"title prefix", `pr.title.startsWith("feat")`, sampleVars(nil), true},
		{"negated label combo", `pr.labels.exists(l, l.name == "cluster/production") && !pr.draft`, sampleVars(nil), true},
		{"createdAt is a timestamp", `pr.createdAt < timestamp("2030-01-01T00:00:00Z")`, sampleVars(nil), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, err := Compile(c.expr)
			if err != nil {
				t.Fatalf("Compile(%q): %v", c.expr, err)
			}
			got, err := p.Eval(c.pr)
			if err != nil {
				t.Fatalf("Eval(%q): %v", c.expr, err)
			}
			if got != c.want {
				t.Fatalf("Eval(%q) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

func TestEval_NonBoolResultErrors(t *testing.T) {
	t.Parallel()
	// pr.title is statically dyn (Compile accepts it) but yields a string at
	// runtime — Eval must reject it, not silently coerce.
	p, err := Compile("pr.title")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if _, err := p.Eval(sampleVars(nil)); err == nil {
		t.Fatal("Eval of a string-valued expression should error")
	}
}

func TestSource(t *testing.T) {
	t.Parallel()
	const expr = "!pr.draft"
	p, err := Compile(expr)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if p.Source() != expr {
		t.Fatalf("Source() = %q, want %q", p.Source(), expr)
	}
}
