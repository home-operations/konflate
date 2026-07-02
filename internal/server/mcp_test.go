package server

import (
	"context"
	"fmt"
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
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, gatusDiffEngine())
	s.refreshList(s.runCtx)
	waitFor(t, s, 7)

	ctx := context.Background()
	cs := mcpClientFor(t, s)

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

// TestMCP_DiffResource exercises the pr-diff resource: the template is advertised,
// get_pr_summary hands back a ResourceLink to it, resources/read returns the full
// rendered diff as plain text (chroma spans stripped, entities unescaped), and an
// unknown or malformed URI is a resource-not-found error.
func TestMCP_DiffResource(t *testing.T) {
	t.Parallel()
	pr := api.PR{Number: 7, Title: "chore(gatus): migrate to gatus-sidecar chart", HeadRef: "feat", BaseRef: "main", HeadSHA: "abc123", Open: true, State: "open"}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: []api.PR{pr}}, gatusDiffEngine())
	s.refreshList(s.runCtx)
	waitFor(t, s, 7)

	ctx := context.Background()
	cs := mcpClientFor(t, s)

	// resources/templates/list advertises the pr-diff template.
	tmpls, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	var hasTemplate bool
	for _, rt := range tmpls.ResourceTemplates {
		if rt.URITemplate == "konflate://pr/{number}/diff" {
			hasTemplate = true
		}
	}
	if !hasTemplate {
		t.Errorf("pr-diff template not advertised: %+v", tmpls.ResourceTemplates)
	}

	// get_pr_summary returns a ResourceLink to the PR's diff resource alongside the
	// Markdown, so the agent can fetch the diff on demand instead of inlining it.
	sum := mustCallTool(t, cs, "get_pr_summary", map[string]any{"number": 7})
	link := firstResourceLink(sum.Content)
	if link == nil || link.URI != "konflate://pr/7/diff" {
		t.Fatalf("summary missing a resource link to konflate://pr/7/diff: %+v", sum.Content)
	}

	// resources/read returns the full diff as plain text — the same rendering
	// get_pr_diff produces: chroma spans stripped, &amp; unescaped, +/- prefixes.
	rr, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: link.URI})
	if err != nil {
		t.Fatalf("ReadResource(%s): %v", link.URI, err)
	}
	text := resourceText(rr)
	for _, want := range []string{"changed Deployment o11y/gatus", "-    app: gatus", "+    app: gatus-sidecar & co"} {
		if !strings.Contains(text, want) {
			t.Errorf("diff resource missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<span") || strings.Contains(text, "&amp;") {
		t.Errorf("diff resource still contains chroma HTML/entities:\n%s", text)
	}

	// An unknown PR and a malformed URI are both resource-not-found errors (returned
	// as transport errors, not an empty read).
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "konflate://pr/999/diff"}); err == nil {
		t.Error("ReadResource(unknown PR): want a not-found error")
	}
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "konflate://pr/notanumber/diff"}); err == nil {
		t.Error("ReadResource(malformed URI): want a not-found error")
	}
}

// TestMCP_Pagination drives list_pull_requests' cursor/limit paging: a bounded page
// carries a continuation cursor, the cursor resumes below the last PR number, the
// final page drops the cursor, an unbounded call shows everything, and a non-numeric
// cursor is a recoverable tool error.
func TestMCP_Pagination(t *testing.T) {
	t.Parallel()
	prs := make([]api.PR, 0, 5)
	for n := 1; n <= 5; n++ {
		prs = append(prs, api.PR{Number: n, Title: fmt.Sprintf("pr %d", n), HeadRef: "f", BaseRef: "main", HeadSHA: "s", Open: true, State: "open"})
	}
	s := newTestServer(t, ghCfg("tok"), &fakeProvider{prs: prs}, okEngine())
	s.refreshList(s.runCtx)
	for n := 1; n <= 5; n++ {
		waitFor(t, s, n)
	}
	cs := mcpClientFor(t, s)

	// First page (limit 2): the two newest, #5 and #4, plus a cursor to continue.
	p1 := toolText(mustCallTool(t, cs, "list_pull_requests", map[string]any{"limit": 2}))
	if !strings.Contains(p1, "5 pull requests tracked (showing 1–2, newest first)") {
		t.Errorf("page 1 header wrong:\n%s", p1)
	}
	for _, want := range []string{"#5 open", "#4 open", `cursor="4"`} {
		if !strings.Contains(p1, want) {
			t.Errorf("page 1 missing %q:\n%s", want, p1)
		}
	}
	if strings.Contains(p1, "#3 open") {
		t.Errorf("page 1 leaked past the limit:\n%s", p1)
	}

	// Second page via the cursor: #3 and #2, then a further cursor.
	p2 := toolText(mustCallTool(t, cs, "list_pull_requests", map[string]any{"limit": 2, "cursor": "4"}))
	for _, want := range []string{"#3 open", "#2 open", `cursor="2"`} {
		if !strings.Contains(p2, want) {
			t.Errorf("page 2 missing %q:\n%s", want, p2)
		}
	}
	if strings.Contains(p2, "#5 open") || strings.Contains(p2, "#1 open") {
		t.Errorf("page 2 out of window:\n%s", p2)
	}

	// Last page: only #1 remains, and no further cursor is offered.
	p3 := toolText(mustCallTool(t, cs, "list_pull_requests", map[string]any{"limit": 2, "cursor": "2"}))
	if !strings.Contains(p3, "#1 open") || strings.Contains(p3, "for the next page") {
		t.Errorf("last page wrong (want #1, no cursor):\n%s", p3)
	}

	// No limit → all five on one page, no window annotation, no cursor footer.
	all := toolText(mustCallTool(t, cs, "list_pull_requests", nil))
	if !strings.Contains(all, "5 pull requests tracked,") || strings.Contains(all, "for the next page") {
		t.Errorf("default page should list all five with no cursor:\n%s", all)
	}

	// A non-numeric cursor is a recoverable tool error, not a transport failure.
	if res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_pull_requests", Arguments: map[string]any{"cursor": "abc"}}); err != nil || !res.IsError {
		t.Errorf("list_pull_requests(cursor=abc): want a tool error (err=%v)", err)
	}
}

// mcpClientFor connects an in-memory MCP client to the server's MCP server and
// registers cleanups to close both sessions.
func mcpClientFor(t *testing.T, s *Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := s.mcpServer().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// gatusDiffEngine renders any PR as a small Deployment diff whose rows carry chroma
// spans and an escaped entity — enough to exercise span-stripping, entity
// unescaping, the +/- prefixes, and a folded-gap marker.
func gatusDiffEngine() *fakeEngine {
	return &fakeEngine{fn: func(pr api.PR) (api.DiffResult, error) {
		return api.DiffResult{
			PRNumber: pr.Number, HeadSHA: "abc123",
			Summary:  api.DiffSummary{Changed: 7, Added: 2, Removed: 2},
			Impact:   api.Impact{Resources: 11, Parents: 2},
			Warnings: []api.Warning{{Level: api.LevelCaution, Rule: "removed-statefulset", Resource: "StatefulSet o11y/gatus", Detail: "its PersistentVolumeClaims and data may be deleted"}},
			Images:   []api.ImageChange{{Name: "ghcr.io/twin/gatus", From: "v5.35.0", To: "v5.36.0"}},
			Resources: []api.DiffResource{{
				ID: "r0", Title: "Deployment o11y/gatus", Kind: "Deployment", Name: "o11y/gatus", Status: "changed", Add: 1, Del: 1,
				Unified: []api.UnifiedRow{
					{Kind: "ctx", HTML: `<span class="ln">  labels:</span>`},
					{Kind: "del", HTML: `<span class="k">    app: gatus</span>`},
					{Kind: "add", HTML: `<span class="k">    app: gatus-sidecar &amp; co</span>`},
					{Hunk: true, Count: 3},
				},
			}},
		}, nil
	}}
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

// firstResourceLink returns the first ResourceLink among the content blocks, or nil.
func firstResourceLink(content []mcp.Content) *mcp.ResourceLink {
	for _, c := range content {
		if rl, ok := c.(*mcp.ResourceLink); ok {
			return rl
		}
	}
	return nil
}

// resourceText concatenates the text of a resources/read result's contents.
func resourceText(rr *mcp.ReadResourceResult) string {
	if rr == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range rr.Contents {
		b.WriteString(c.Text)
	}
	return b.String()
}

// TestWritePRLine_FlattensTitle: the MCP list is one line per PR, parsed by line,
// so a forge-controlled title with a newline/control char must be flattened.
func TestWritePRLine_FlattensTitle(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	writePRLine(&b, api.PRStatus{
		PR:     api.PR{Number: 7, Title: "line one\nline two\r\tmore", Open: true},
		Status: api.JobReady,
	})
	out := b.String()
	if n := strings.Count(out, "\n"); n != 1 || !strings.HasSuffix(out, "\n") {
		t.Errorf("title not flattened to a single line (%d newlines): %q", n, out)
	}
	if strings.Contains(out, "line one\nline two") {
		t.Errorf("a raw newline survived into the listing: %q", out)
	}
}

// TestRenderDiffText_TruncatesLargeDiff: a diff far larger than the MCP byte
// budget is truncated (with a note) rather than fully stripped-then-discarded.
func TestRenderDiffText_TruncatesLargeDiff(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 4096) // plain text: stripHTML is a no-op
	var resources []api.DiffResource
	for i := 0; i < 64; i++ { // ~256 KiB, well over the 96 KiB budget
		resources = append(resources, api.DiffResource{
			Status:  "changed",
			Title:   fmt.Sprintf("Deployment ns/app-%d", i),
			Unified: []api.UnifiedRow{{Kind: "ctx", HTML: big}},
		})
	}
	out := renderDiffText(resources, 0)
	if len(out) > mcpDiffMaxBytes+1024 {
		t.Errorf("output not truncated to the budget: %d bytes", len(out))
	}
	if !strings.Contains(out, "output truncated") {
		t.Errorf("truncated output is missing the note:\n…%s", out[max(0, len(out)-160):])
	}
}
