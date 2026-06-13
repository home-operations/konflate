package server

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/home-operations/konflate/internal/api"
)

// mcpHandler builds the read-only Model Context Protocol endpoint served at /mcp
// when KONFLATE_MCP is set. It exposes konflate's rendered-diff analysis — the open
// pull requests and their summaries — as MCP tools, so an AI agent reviewing a Flux
// PR can see what the change actually does to the cluster (the blast radius,
// cautions, image changes) that a raw git diff hides. The tools read the same
// in-memory store the JSON API serves and trigger no render and no forge write, so
// /mcp is read-only exactly like /api.
func (s *Server) mcpHandler() http.Handler {
	// One server instance serves every request; the SDK tracks per-client sessions.
	srv := s.mcpServer()
	// CrossOriginProtection rejects cross-origin browser requests (via Sec-Fetch-Site
	// / Origin) as CSRF defense-in-depth; native MCP clients send neither header and
	// are unaffected. The SDK additionally rejects DNS-rebinding (a non-localhost Host
	// on a localhost request) by default.
	return mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{CrossOriginProtection: http.NewCrossOriginProtection()},
	)
}

// mcpServer builds the MCP server and registers konflate's read-only tools. Split
// from mcpHandler so tests can drive it over an in-memory transport.
func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "konflate",
		Title:   "konflate — rendered Flux PR diffs",
		Version: cmp.Or(s.Version, "dev"),
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_pull_requests",
		Description: "List the open pull requests konflate is tracking. Each carries its " +
			"rendered-diff signals (changed resources, cautions, image changes, render " +
			"failures) and CI check status. Use it to find pull requests, then call " +
			"get_pr_summary for one PR's details.",
	}, s.mcpListPRs)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_pr_summary",
		Description: "konflate's rendered-diff summary for one pull request, by number: the " +
			"blast radius, caution lint (data-loss, immutable-field, RBAC, suspend/prune), " +
			"image changes, and render failures — the cluster impact a raw git diff doesn't " +
			"show. Returns Markdown.",
	}, s.mcpPRSummary)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_pr_diff",
		Description: "The rendered diff for a pull request as a plain-text unified diff — the " +
			"actual Kubernetes YAML konflate produced at the PR head vs its merge-base, per " +
			"changed resource (+ added / - removed lines). This can be large; pass `resource` " +
			"(a resource id like \"r0\", or a substring of its title) to fetch one resource at " +
			"a time. Available only once the PR has finished rendering.",
	}, s.mcpPRDiff)

	return srv
}

// --- list_pull_requests ---

// mcpListPRs returns the tracked PRs as a compact, one-line-per-PR text block.
// Plain text (not per-field JSON) keeps the token cost down — no repeated keys or
// braces — and zero-valued signals are omitted, so a clean PR adds no noise. It's
// returned as a single text content, so every MCP client sees the same thing
// (no reliance on whether a client surfaces structured output). An agent then
// calls get_pr_summary or get_pr_diff for one PR's detail.
func (s *Server) mcpListPRs(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
	list := s.store.list()
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s tracked, one per line — #num open|merged|closed "+
		"[render: pending|running|error, omitted once the diff is ready] [ci=…] [signals] title\n",
		len(list), plural(len(list), "pull request", "pull requests"))
	for _, p := range list {
		writePRLine(&b, p)
	}
	return mcpText(b.String()), nil, nil
}

// writePRLine renders one tracked PR as a compact line: number, lifecycle, the
// render status only while it's noteworthy (open PRs that aren't ready yet — a
// ready diff is the steady state, and a merged PR's diff is frozen), draft/hidden/
// CI flags when set, the non-zero signals, then the title.
func writePRLine(b *strings.Builder, p api.PRStatus) {
	fmt.Fprintf(b, "#%d %s", p.Number, prStateWord(p))
	if p.Open && p.Status != api.JobReady {
		fmt.Fprintf(b, " %s", p.Status) // pending | running | error
	}
	if p.Draft {
		b.WriteString(" draft")
	}
	if p.Hidden {
		b.WriteString(" hidden") // filtered or a fork — listed, never rendered
	}
	if p.Checks != nil && p.Checks.State != "" {
		fmt.Fprintf(b, " ci=%s", p.Checks.State)
	}
	if sig := signalSummary(p.Signals); sig != "" {
		fmt.Fprintf(b, " [%s]", sig)
	}
	fmt.Fprintf(b, " %s\n", p.Title)
}

// signalSummary joins the non-zero rendered-diff signals (omitting any that are
// zero), e.g. "11 resources, 1 caution, 1 image change, 1 render failure".
func signalSummary(s *api.Signals) string {
	if s == nil {
		return ""
	}
	var parts []string
	if s.Resources > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", s.Resources, plural(s.Resources, "resource", "resources")))
	}
	if s.Caution > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", s.Caution, plural(s.Caution, "caution", "cautions")))
	}
	if s.Images > 0 {
		parts = append(parts, fmt.Sprintf("%d image %s", s.Images, plural(s.Images, "change", "changes")))
	}
	if s.Failures > 0 {
		parts = append(parts, fmt.Sprintf("%d render %s", s.Failures, plural(s.Failures, "failure", "failures")))
	}
	return strings.Join(parts, ", ")
}

// Normalized PR lifecycle words for the agent-facing view. The forge state string
// (PR.State) differs across forges — GitHub "open", GitLab "opened" — so derive
// the word from konflate's normalized flags instead.
const (
	stateOpen   = "open"
	stateMerged = "merged"
	stateClosed = "closed"
)

// prStateWord normalizes a PR's lifecycle to open | merged | closed for the agent.
func prStateWord(p api.PRStatus) string {
	switch {
	case p.Open:
		return stateOpen
	case p.Merged:
		return stateMerged
	default:
		return stateClosed
	}
}

// --- get_pr_summary ---

type mcpSummaryInput struct {
	Number int `json:"number" jsonschema:"the pull request number"`
}

func (s *Server) mcpPRSummary(_ context.Context, _ *mcp.CallToolRequest, in mcpSummaryInput) (*mcp.CallToolResult, any, error) {
	env, ok := s.store.get(in.Number)
	if !ok {
		return mcpError("No pull request #%d is tracked.", in.Number), nil, nil
	}
	// The same Markdown summary konflate posts as a PR comment — GitHub-flavoured
	// admonitions render in a Markdown-aware agent and degrade to plain text.
	return mcpText(summaryMarkdownBody(env, s.reviewURL(in.Number), true)), nil, nil
}

// --- get_pr_diff ---

type mcpDiffInput struct {
	Number   int    `json:"number" jsonschema:"the pull request number"`
	Resource string `json:"resource,omitempty" jsonschema:"optional: a resource id (e.g. r0) or a title substring; omit for the full diff"`
}

// mcpDiffMaxBytes caps the diff text a single tool call returns. A sweeping PR's
// full diff can be multi-MB; past this the agent is told to request one resource.
const mcpDiffMaxBytes = 96 << 10

func (s *Server) mcpPRDiff(_ context.Context, _ *mcp.CallToolRequest, in mcpDiffInput) (*mcp.CallToolResult, any, error) {
	env, ok := s.store.get(in.Number)
	if !ok {
		return mcpError("No pull request #%d is tracked.", in.Number), nil, nil
	}
	if env.Diff == nil {
		// Pending/running/hidden (a filtered or fork PR is never rendered) or a
		// failed render — there's no diff to return. Reading the store renders
		// nothing, so this can't be used to force-render a hidden fork.
		if env.Status == api.JobError && env.Error != "" {
			return mcpError("PR #%d failed to render: %s", in.Number, oneLine(env.Error)), nil, nil
		}
		return mcpError("PR #%d has no rendered diff yet (status %q).", in.Number, env.Status), nil, nil
	}

	resources := env.Diff.Resources
	if in.Resource != "" {
		resources = filterResources(resources, in.Resource)
		if len(resources) == 0 {
			return mcpError("No resource matching %q in PR #%d. Available: %s",
				in.Resource, in.Number, availableResources(env.Diff.Resources)), nil, nil
		}
	}

	var b strings.Builder
	for i := range resources {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeResourceDiff(&b, resources[i])
	}
	body := b.String()

	var notes []string
	if len(body) > mcpDiffMaxBytes {
		cut := body[:mcpDiffMaxBytes]
		if nl := strings.LastIndexByte(cut, '\n'); nl > 0 {
			cut = cut[:nl]
		}
		body = cut
		notes = append(notes, "… output truncated; pass the `resource` argument to fetch a single resource …")
	}
	if in.Resource == "" && env.Diff.Truncated > 0 {
		notes = append(notes, fmt.Sprintf("… %d more changed %s omitted by the render cap (KONFLATE_MAX_DIFF_RESOURCES) …",
			env.Diff.Truncated, plural(env.Diff.Truncated, "resource", "resources")))
	}
	if len(notes) > 0 {
		body += "\n" + strings.Join(notes, "\n") + "\n"
	}
	return mcpText(body), nil, nil
}

// writeResourceDiff renders one resource as a plain-text unified diff: a heading,
// then +/- /context lines with the chroma highlight stripped back to plain text.
// Folded (collapsed-context) rows are dropped; a fold separator becomes a count.
func writeResourceDiff(b *strings.Builder, r api.DiffResource) {
	fmt.Fprintf(b, "%s %s\n", r.Status, r.Title)
	for _, row := range r.Unified {
		switch {
		case row.Folded:
			continue
		case row.Hunk:
			fmt.Fprintf(b, "    … %d unchanged %s …\n", row.Count, plural(row.Count, "line", "lines"))
		default:
			b.WriteByte(diffLinePrefix(row.Kind))
			b.WriteString(stripHTML(row.HTML))
			b.WriteByte('\n')
		}
	}
}

func diffLinePrefix(kind string) byte {
	switch kind {
	case "add":
		return '+'
	case "del":
		return '-'
	default:
		return ' '
	}
}

// htmlTag matches a single HTML tag. konflate stores each diff line as chroma
// output, which HTML-escapes all token text — so a raw '<' only ever appears as a
// tag delimiter here, making tag-stripping safe.
var htmlTag = regexp.MustCompile(`<[^>]*>`)

// stripHTML turns one chroma-highlighted line back into plain text: drop the
// <span> tags, then unescape the entities chroma wrote for <, >, and &.
func stripHTML(s string) string {
	return html.UnescapeString(htmlTag.ReplaceAllString(s, ""))
}

// filterResources keeps resources whose id equals q or whose title contains q
// (case-insensitive), so an agent can scope to one resource by either.
func filterResources(rs []api.DiffResource, q string) []api.DiffResource {
	ql := strings.ToLower(q)
	out := rs[:0:0]
	for _, r := range rs {
		if r.ID == q || strings.Contains(strings.ToLower(r.Title), ql) {
			out = append(out, r)
		}
	}
	return out
}

// availableResources lists the resource ids+titles for a "no match" error, capped
// so a sweeping PR doesn't blow up the message.
func availableResources(rs []api.DiffResource) string {
	const max = 25
	parts := make([]string, 0, min(len(rs), max))
	for i, r := range rs {
		if i == max {
			parts = append(parts, fmt.Sprintf("… and %d more", len(rs)-max))
			break
		}
		parts = append(parts, fmt.Sprintf("%q (%s)", r.Title, r.ID))
	}
	return strings.Join(parts, ", ")
}

// --- shared tool-result helpers ---

func mcpText(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// mcpError is a tool-level error (IsError) the model sees and can recover from —
// e.g. a wrong PR number — as opposed to a transport/protocol failure.
func mcpError(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
