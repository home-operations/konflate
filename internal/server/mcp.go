package server

import (
	"cmp"
	"context"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
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
		Description: "List the pull requests konflate is tracking, newest first. Each carries " +
			"its rendered-diff signals (changed resources, cautions, image changes, render " +
			"failures) and CI check status. Paginated: a page's footer gives a `cursor` to " +
			"pass back for the next page. Use it to find pull requests, then call " +
			"get_pr_summary for one PR's details.",
	}, s.mcpListPRs)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_pr_summary",
		Description: "konflate's rendered-diff summary for one pull request, by number: the " +
			"blast radius, caution lint (data-loss, immutable-field, RBAC, suspend/prune), " +
			"image changes, and render failures — the cluster impact a raw git diff doesn't " +
			"show. Returns Markdown plus a link to the PR's full-diff resource.",
	}, s.mcpPRSummary)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_pr_diff",
		Description: "The rendered diff for a pull request as a plain-text unified diff — the " +
			"actual Kubernetes YAML konflate produced at the PR head vs its merge-base, per " +
			"changed resource (+ added / - removed lines). This can be large; pass `resource` " +
			"(a resource id like \"r0\", or a substring of its title) to fetch one resource at " +
			"a time. The whole diff is also the resource konflate://pr/<number>/diff. Available " +
			"only once the PR has finished rendering.",
	}, s.mcpPRDiff)

	// PR diffs are also addressable as an MCP resource — konflate://pr/{number}/diff
	// — so a client can fetch (or @-mention) a diff on demand instead of get_pr_diff
	// inlining it into the conversation; get_pr_summary hands back a link to it. One
	// template serves every PR (the tracked set is dynamic), matched by the SDK on
	// read — konflate never enumerates a resource per PR, which would churn the
	// registry and emit a list_changed notification on every poll.
	srv.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "pr-diff",
		Title:       "Rendered PR diff",
		MIMEType:    mimeTextPlain,
		URITemplate: "konflate://pr/{number}/diff",
		Description: "The full rendered diff for a pull request as a plain-text unified diff — " +
			"the same content get_pr_diff returns with no resource filter. Read " +
			"konflate://pr/<number>/diff, the number from list_pull_requests.",
	}, s.mcpReadDiffResource)

	return srv
}

// mimeTextPlain is the MIME type for konflate's plain-text diff output (the tools'
// stripped unified diff and the pr-diff resource).
const mimeTextPlain = "text/plain"

// --- list_pull_requests ---

type mcpListInput struct {
	Cursor string `json:"cursor,omitempty" jsonschema:"continuation cursor from a previous page's footer; omit for the first (newest) page"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max pull requests to return (default 100); the rest are paged behind a cursor"`
}

// mcpListDefaultLimit bounds one page of list_pull_requests when the caller gives
// no limit, so a repo with hundreds of open PRs doesn't dump them all into the
// agent's context at once.
const mcpListDefaultLimit = 100

// mcpListPRs returns a page of tracked PRs as a compact, one-line-per-PR text
// block. Plain text (not per-field JSON) keeps the token cost down — no repeated
// keys or braces — and zero-valued signals are omitted, so a clean PR adds no
// noise. It's returned as a single text content, so every MCP client sees the same
// thing (no reliance on whether a client surfaces structured output). The list is
// paginated newest-first; an agent then calls get_pr_summary or get_pr_diff for one
// PR's detail.
func (s *Server) mcpListPRs(_ context.Context, _ *mcp.CallToolRequest, in mcpListInput) (*mcp.CallToolResult, any, error) {
	list := s.store.list() // newest first by number — a stable, total order
	limit := in.Limit
	if limit <= 0 {
		limit = mcpListDefaultLimit
	}

	// The cursor is the last PR number emitted on the previous page; since the list
	// descends by number, resume at the first PR below it. Number-keyed (not index-
	// keyed) so a PR opening or closing between pages can't skip or repeat a row.
	start := 0
	if in.Cursor != "" {
		after, err := strconv.Atoi(in.Cursor)
		if err != nil {
			return mcpError("Invalid cursor %q — pass the value from a previous page's footer.", in.Cursor), nil, nil
		}
		for start < len(list) && list[start].Number >= after {
			start++
		}
	}
	page := list[start:]
	more := len(page) > limit
	if more {
		page = page[:limit]
	}

	var b strings.Builder
	writeListHeader(&b, len(list), start, len(page))
	for _, p := range page {
		writePRLine(&b, p)
	}
	if more {
		fmt.Fprintf(&b, "… %d more; call list_pull_requests with cursor=%q for the next page …\n",
			len(list)-start-len(page), strconv.Itoa(page[len(page)-1].Number))
	}
	return mcpText(b.String()), nil, nil
}

// writeListHeader writes the count line and the column legend. It names the page
// window only when the list is actually paged, so the common single-page case
// stays terse.
func writeListHeader(b *strings.Builder, total, start, shown int) {
	fmt.Fprintf(b, "%d %s tracked", total, plural(total, "pull request", "pull requests"))
	if shown > 0 && (start > 0 || start+shown < total) {
		fmt.Fprintf(b, " (showing %d–%d, newest first)", start+1, start+shown)
	}
	b.WriteString(", one per line — #num open|merged|closed " +
		"[render: pending|running|error, omitted once the diff is ready] [ci=…] [signals] title\n")
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
	// mdListText defangs the forge-controlled title: it flattens control chars
	// (which would break this one-line-per-PR listing) and escapes the link/image
	// brackets so a crafted title can't inject a clickable link or remote image
	// into an MCP client that renders the tool text as Markdown.
	fmt.Fprintf(b, " %s\n", mdListText(p.Title))
}

// mdListText prepares forge-controlled text for a one-line MCP listing: oneLine
// flattens control characters, then the escapes neutralise the Markdown/HTML that
// could inject content into a client rendering the tool text — the link/image
// brackets ([text](url), ![alt](url)), the code-span backtick, and the angle
// brackets (an <url> autolink or a raw <script> HTML tag). Ordinary punctuation
// (parentheses, underscores, *) is left so a normal title like "feat(scope): …"
// reads cleanly; a bare autolinked URL is left too — its destination is visible,
// so it isn't the spoofing vector a hidden-destination link is (same stance as
// mdInline).
func mdListText(s string) string {
	return mdListReplacer.Replace(oneLine(s))
}

var mdListReplacer = strings.NewReplacer("[", `\[`, "]", `\]`, "`", "\\`", "<", `\<`, ">", `\>`)

// signalSummary joins the non-zero rendered-diff signals (omitting any that are
// zero), e.g. "11 resources, 1 blocker, 1 caution, 1 image change, 1 render failure".
func signalSummary(s *api.Signals) string {
	if s == nil {
		return ""
	}
	var parts []string
	if s.Resources > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", s.Resources, plural(s.Resources, "resource", "resources")))
	}
	if s.Blocking > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", s.Blocking, plural(s.Blocking, "blocker", "blockers")))
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
	content := []mcp.Content{&mcp.TextContent{Text: summaryMarkdownBody(env, s.reviewURL(in.Number), true)}}
	// Hand back a link to the full diff as a resource — but only once it exists, so
	// the link never dangles — letting the agent fetch the diff on demand instead of
	// the summary inlining it.
	if env.Diff != nil {
		content = append(content, prDiffLink(in.Number))
	}
	return &mcp.CallToolResult{Content: content}, nil, nil
}

// prDiffLink is a ResourceLink to a PR's rendered-diff resource, for tool results
// that want to point at the full diff without inlining it.
func prDiffLink(number int) *mcp.ResourceLink {
	return &mcp.ResourceLink{
		URI:      prDiffURI(number),
		Name:     fmt.Sprintf("pr-%d-diff", number),
		Title:    fmt.Sprintf("Rendered diff for PR #%d", number),
		MIMEType: mimeTextPlain,
		Description: "The full rendered YAML diff as plain text — read this resource " +
			"(or call get_pr_diff) for the line-level changes.",
	}
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
	renderTruncated := env.Diff.Truncated
	if in.Resource != "" {
		resources = filterResources(resources, in.Resource)
		if len(resources) == 0 {
			return mcpError("No resource matching %q in PR #%d. Available: %s",
				in.Resource, in.Number, availableResources(env.Diff.Resources)), nil, nil
		}
		renderTruncated = 0 // a scoped view isn't "missing" the render-capped resources
	}
	return mcpText(renderDiffText(resources, renderTruncated)), nil, nil
}

// renderDiffText renders the resources to a capped plain-text unified diff,
// appending notes when the byte cap clips the output or when the render itself
// dropped resources (KONFLATE_MAX_DIFF_RESOURCES). Shared by the get_pr_diff tool
// and the pr-diff resource so both stay consistent and bounded.
func renderDiffText(resources []api.DiffResource, renderTruncated int) string {
	var b strings.Builder
	overflow := false
	for i := range resources {
		if i > 0 {
			b.WriteByte('\n')
		}
		writeResourceDiff(&b, resources[i])
		// Stop once the budget is filled: otherwise a multi-MB diff strips every
		// row's chroma HTML back to text only to discard all but the first 96 KiB.
		if b.Len() >= mcpDiffMaxBytes {
			overflow = i < len(resources)-1 // resources we won't render
			break
		}
	}
	body := b.String()

	var notes []string
	if len(body) > mcpDiffMaxBytes {
		cut := body[:mcpDiffMaxBytes]
		if nl := strings.LastIndexByte(cut, '\n'); nl > 0 {
			cut = cut[:nl]
		}
		body = cut
		overflow = true
	}
	if overflow {
		notes = append(notes, "… output truncated; use get_pr_diff with the `resource` argument to fetch one resource at a time …")
	}
	if renderTruncated > 0 {
		notes = append(notes, fmt.Sprintf("… %d more changed %s omitted by the render cap (KONFLATE_MAX_DIFF_RESOURCES) …",
			renderTruncated, plural(renderTruncated, "resource", "resources")))
	}
	if len(notes) > 0 {
		body += "\n" + strings.Join(notes, "\n") + "\n"
	}
	return body
}

// --- pr-diff resource (konflate://pr/{number}/diff) ---

// prDiffURI builds the resource URI for a PR's rendered diff.
func prDiffURI(number int) string {
	return fmt.Sprintf("konflate://pr/%d/diff", number)
}

// prDiffURIRe re-parses a pr-diff URI strictly. The SDK routes any URI matching the
// registered template ({number} accepts any non-slash run) to the handler, so we
// re-check here: a non-numeric or malformed URI is a clean not-found.
var prDiffURIRe = regexp.MustCompile(`^konflate://pr/(\d+)/diff$`)

// mcpReadDiffResource serves konflate://pr/{number}/diff: the full rendered diff
// for one PR as plain text, the same content get_pr_diff returns with no filter.
// It reads the store only (renders nothing), so an unrendered, hidden, or unknown
// PR is a not-found — never a way to force a fork to render.
func (s *Server) mcpReadDiffResource(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	uri := req.Params.URI
	m := prDiffURIRe.FindStringSubmatch(uri)
	if m == nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	number, err := strconv.Atoi(m[1])
	if err != nil { // an over-long digit run overflows int
		return nil, mcp.ResourceNotFoundError(uri)
	}
	env, ok := s.store.get(number)
	if !ok || env.Diff == nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: mimeTextPlain,
			Text:     renderDiffText(env.Diff.Resources, env.Diff.Truncated),
		}},
	}, nil
}

// writeResourceDiff renders one resource as a plain-text unified diff: a heading,
// then +/- /context lines with the chroma highlight stripped back to plain text.
// Folded (collapsed-context) rows are dropped; a fold separator becomes a count.
func writeResourceDiff(b *strings.Builder, r api.DiffResource) {
	fmt.Fprintf(b, "%s %s\n", r.Status, r.Title)
	for _, row := range r.Unified {
		if b.Len() >= mcpDiffMaxBytes {
			break // a single huge resource: don't strip past the budget renderDiffText will trim to
		}
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
