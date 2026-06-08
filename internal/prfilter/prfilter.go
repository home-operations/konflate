// Package prfilter compiles and evaluates a CEL boolean expression that decides
// which pull requests konflate tracks (KONFLATE_PR_FILTER_EXPR).
//
// The expression sees a single variable, pr — a map of the PR's fields. The
// caller supplies that map (the server builds it from api.PR), so this package
// stays decoupled from the forge model and is testable with plain maps. An
// expression must evaluate to a boolean; the program is type-checked once at
// Compile so a malformed filter fails fast at startup rather than per request.
//
// CEL is the right tool here: it's a safe, bounded, non-Turing-complete
// expression language (no I/O, no unbounded loops), so an operator-supplied
// predicate can't hang or escape — and the home-ops/Kubernetes audience already
// knows it from admission policies.
package prfilter

import (
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// Program is a compiled, type-checked PR-filter expression.
type Program struct {
	prg cel.Program
	src string
}

// newEnv builds the CEL environment: a single pr variable, a string-keyed map of
// dynamic values. Kept in one place so Compile and any future tooling agree on
// the exposed shape.
func newEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("pr", cel.MapType(cel.StringType, cel.DynType)),
	)
}

// Compile parses and type-checks expr and returns a runnable Program. It fails
// when the expression is syntactically invalid, references unknown
// variables/functions, or cannot produce a boolean.
func Compile(expr string) (*Program, error) {
	env, err := newEnv()
	if err != nil {
		return nil, fmt.Errorf("prfilter: build env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("prfilter: %w", iss.Err())
	}
	// The filter is a yes/no decision. Field access on the dyn-valued pr map is
	// statically dyn (only known boolean at runtime), so accept bool or dyn here;
	// a literal of the wrong type (e.g. 42, "x") is statically typed and rejected.
	// Eval enforces an actual boolean result.
	switch ast.OutputType().Kind() {
	case types.BoolKind, types.DynKind:
	default:
		return nil, fmt.Errorf("prfilter: expression must evaluate to a boolean, got %s", ast.OutputType())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("prfilter: program: %w", err)
	}
	return &Program{prg: prg, src: expr}, nil
}

// Eval runs the expression against the given pr field map and reports whether
// the PR is allowed. A runtime error or a non-boolean result is returned as an
// error (the caller decides the fail-safe — konflate drops the PR and logs).
func (p *Program) Eval(pr map[string]any) (bool, error) {
	out, _, err := p.prg.Eval(map[string]any{"pr": pr})
	if err != nil {
		return false, fmt.Errorf("prfilter: eval %q: %w", p.src, err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("prfilter: %q produced %T, want bool", p.src, out.Value())
	}
	return b, nil
}

// Source returns the original expression text (for logs and diagnostics).
func (p *Program) Source() string { return p.src }
