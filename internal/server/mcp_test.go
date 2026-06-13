package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/home-operations/konflate/internal/api"
)

// TestMCP_Tools drives the read-only MCP server over an in-memory transport: the
// three tools are advertised, list_pull_requests returns the tracked PR as a
// compact one-line text block carrying its rendered-diff signals, get_pr_summary
// returns the Markdown overview, get_pr_diff returns the rendered YAML as a
// plain-text unified diff, and an unknown PR number is a tool-level error the
// model can recover from.
func TestMCP_Tools(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 7, Title: "chore(gatus): migrate to gatus-sidecar chart", HeadRef: "feat", BaseRef: "main", HeadSHA: "abc123", Open: true, State: "open"}
	rich := &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{
			PRNumber: pr.Number, HeadSHA: "abc123",
			Summary:  api.DiffSummary{Changed: 7, Added: 2, Removed: 2},
			Impact:   api.Impact{Resources: 11, Parents: 2},
			Warnings: []api.Warning{{Level: api.LevelCaution, Rule: "removed-statefulset", Resource: "StatefulSet o11y/gatus", Detail: "its PersistentVolumeClaims and data may be deleted"}},
			Images:   []api.ImageChange{{Name: "ghcr.io/twin/gatus", From: "v5.35.0", To: "v5.36.0"}},
			Resources: []api.DiffResource{{
				ID: "r0", Title: "Deployment o11y/gatus", Kind: "Deployment", Name: "o11y/gatus", Status: "changed", Add: 1, Del: 1,
				// chroma-style highlighted rows; one carries an escaped entity to prove
				// the tool strips spans and unescapes &amp; back to plain text.
				Unified: []api.UnifiedRow{
					{Kind: "ctx", HTML: `<span class="ln">  labels:</span>`},
					{Kind: "del", HTML: `<span class="k">    app: gatus</span>`},
					{Kind: "add", HTML: `<span class="k">    app: gatus-sidecar &amp; co</span>`},
					{Hunk: true, Count: 3},
				},
			}},
		}, nil
	}}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, rich)
	s.refreshList(s.runCtx)
	waitFor(t, s, 7)

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := s.mcpServer().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	// tools/list advertises exactly the three read-only tools.
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	if !names["list_pull_requests"] || !names["get_pr_summary"] || !names["get_pr_diff"] {
		t.Fatalf("advertised tools = %v, want list_pull_requests + get_pr_summary + get_pr_diff", names)
	}

	// list_pull_requests: a compact one-line-per-PR text block — lifecycle, the
	// non-zero signals in order, then the title. Plain text, no per-field JSON, to
	// keep the token cost down. The "ready" render state is the steady state and is
	// omitted (the full-line match below would break if "ready" leaked in).
	list := toolText(mustCallTool(t, cs, "list_pull_requests", nil))
	if !strings.Contains(list, "1 pull request tracked") {
		t.Errorf("list header missing the PR count:\n%s", list)
	}
	const wantLine = "#7 open [1 resource, 1 caution, 1 image change] chore(gatus): migrate to gatus-sidecar chart"
	if !strings.Contains(list, wantLine) {
		t.Errorf("list line missing or malformed; want %q in:\n%s", wantLine, list)
	}

	// get_pr_summary: the Markdown overview, carrying the caution.
	if text := toolText(mustCallTool(t, cs, "get_pr_summary", map[string]any{"number": 7})); !strings.Contains(text, "konflate — summary") || !strings.Contains(text, "gatus") {
		t.Errorf("summary missing expected content:\n%s", text)
	}

	// get_pr_diff: the rendered YAML as a plain-text unified diff — chroma spans
	// stripped, entities unescaped, +/- prefixes, and a folded-gap marker.
	diff := toolText(mustCallTool(t, cs, "get_pr_diff", map[string]any{"number": 7}))
	for _, want := range []string{
		"changed Deployment o11y/gatus",
		"-    app: gatus",
		"+    app: gatus-sidecar & co", // &amp; unescaped, <span> stripped
		"… 3 unchanged lines …",
	} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "<span") || strings.Contains(diff, "&amp;") {
		t.Errorf("diff still contains chroma HTML/entities:\n%s", diff)
	}

	// The resource filter scopes to one resource.
	if got := toolText(mustCallTool(t, cs, "get_pr_diff", map[string]any{"number": 7, "resource": "r0"})); !strings.Contains(got, "Deployment o11y/gatus") {
		t.Errorf("get_pr_diff(7, resource=r0):\n%s", got)
	}

	// Recoverable tool errors (IsError), not transport failures: an unknown PR
	// number, and an unmatched resource.
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_pr_summary", Arguments: map[string]any{"number": 999}}); err != nil || !res.IsError {
		t.Errorf("get_pr_summary(999): want a tool error (err=%v)", err)
	}
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_pr_diff", Arguments: map[string]any{"number": 7, "resource": "does-not-exist"}}); err != nil || !res.IsError {
		t.Errorf("get_pr_diff(unmatched resource): want a tool error (err=%v)", err)
	}
}

// mustCallTool calls a tool and fails the test on a transport error or a
// tool-level error result, returning the successful result.
func mustCallTool(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, toolText(res))
	}
	return res
}

// TestMCP_HTTPEndpoint exercises the real /mcp wiring end to end: the route is
// served only when KONFLATE_MCP is set, and over the streamable HTTP transport
// (through the full middleware stack — so this also guards that statusRecorder's
// Unwrap keeps SSE flushing working) a client can call a tool.
func TestMCP_HTTPEndpoint(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 7, Title: "feat: widget", HeadRef: "feat", BaseRef: "main", HeadSHA: "abc123", Open: true, State: "open"}

	t.Run("served and callable when enabled", func(t *testing.T) {
		t.Parallel()
		cfg := ghCfg("tok")
		cfg.MCP = true
		s := newTestServer(t, cfg, &fakeProvider{prs: []api.PR{pr}}, okEngine())
		s.refreshList(s.runCtx)
		waitFor(t, s, 7)

		httpSrv := httptest.NewServer(s.mainHandler())
		t.Cleanup(httpSrv.Close)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).
			Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpSrv.URL + "/mcp"}, nil)
		if err != nil {
			t.Fatalf("connect /mcp: %v", err)
		}
		defer func() { _ = cs.Close() }()

		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_pull_requests"})
		if err != nil || res.IsError {
			t.Fatalf("list_pull_requests over HTTP: err=%v isError=%v", err, res != nil && res.IsError)
		}
		if text := toolText(res); !strings.Contains(text, "#7") {
			t.Fatalf("over HTTP, list_pull_requests text = %q, want it to name #7", text)
		}
	})

	t.Run("not served when disabled (the default)", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, okEngine()) // cfg.MCP is false
		httpSrv := httptest.NewServer(s.mainHandler())
		t.Cleanup(httpSrv.Close)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// /mcp isn't registered, so the initialize handshake hits no MCP endpoint
		// and Connect fails — rather than exposing the surface by default.
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).
			Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpSrv.URL + "/mcp"}, nil)
		if err == nil {
			_ = cs.Close()
			t.Fatal("connected to /mcp with KONFLATE_MCP off; the endpoint must not be served")
		}
	})
}

// toolText concatenates the text content blocks of a tool result.
func toolText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
