package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/home-operations/konflate/internal/api"
)

// TestMCP_Tools drives the read-only MCP server over an in-memory transport: the
// two tools are advertised, list_pull_requests returns the tracked PR with its
// rendered-diff signals, get_pr_summary returns the Markdown overview, and an
// unknown PR number is a tool-level error the model can recover from.
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

	// tools/list advertises exactly the two read-only tools.
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools.Tools {
		names[tl.Name] = true
	}
	if !names["list_pull_requests"] || !names["get_pr_summary"] {
		t.Fatalf("advertised tools = %v, want list_pull_requests + get_pr_summary", names)
	}

	// list_pull_requests: the tracked PR with its computed signals (1 caution, 1 image).
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_pull_requests"})
	if err != nil || res.IsError {
		t.Fatalf("list_pull_requests: err=%v isError=%v (%s)", err, res != nil && res.IsError, toolText(res))
	}
	var out mcpListOutput
	decodeStructured(t, res.StructuredContent, &out)
	if len(out.PullRequests) != 1 {
		t.Fatalf("listed %d PRs, want 1: %+v", len(out.PullRequests), out.PullRequests)
	}
	if got := out.PullRequests[0]; got.Number != 7 || got.State != "open" || got.Cautions != 1 || got.ImageChanges != 1 {
		t.Errorf("PR view = %+v, want #7 open with 1 caution + 1 image change", got)
	}

	// get_pr_summary: the Markdown overview, carrying the caution.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_pr_summary", Arguments: map[string]any{"number": 7}})
	if err != nil || res.IsError {
		t.Fatalf("get_pr_summary(7): err=%v isError=%v (%s)", err, res != nil && res.IsError, toolText(res))
	}
	if text := toolText(res); !strings.Contains(text, "konflate — summary") || !strings.Contains(text, "gatus") {
		t.Errorf("summary missing expected content:\n%s", text)
	}

	// An unknown PR number is a tool-level error (IsError), not a transport failure.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_pr_summary", Arguments: map[string]any{"number": 999}})
	if err != nil {
		t.Fatalf("get_pr_summary(999) transport error: %v", err)
	}
	if !res.IsError {
		t.Error("get_pr_summary on an unknown PR should return a tool error")
	}
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

// decodeStructured round-trips a tool's structured output (delivered to the client
// as a decoded JSON value) into a typed struct.
func decodeStructured(t *testing.T, v any, dst any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
}
