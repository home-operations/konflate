package server

import (
	"cmp"
	"context"
	"fmt"
	"net/http"

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

	return srv
}

// --- list_pull_requests ---

type mcpListOutput struct {
	PullRequests []mcpPR `json:"pullRequests"`
}

// mcpPR is the agent-facing view of a tracked PR: its identity plus the
// rendered-diff signals, minus the UI-only fields (avatar proxy, merge command).
type mcpPR struct {
	Number          int    `json:"number"`
	Title           string `json:"title"`
	Author          string `json:"author,omitempty"`
	State           string `json:"state"`        // open | merged | closed
	RenderStatus    string `json:"renderStatus"` // pending | running | ready | error
	URL             string `json:"url,omitempty"`
	Draft           bool   `json:"draft,omitempty"`
	Hidden          bool   `json:"hidden,omitempty"` // excluded by the PR filter — listed, never rendered
	ResourceChanges int    `json:"resourceChanges"`
	Cautions        int    `json:"cautions"`
	ImageChanges    int    `json:"imageChanges"`
	RenderFailures  int    `json:"renderFailures"`
	CIChecks        string `json:"ciChecks,omitempty"` // success | failure | pending
}

func (s *Server) mcpListPRs(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpListOutput, error) {
	list := s.store.list()
	out := mcpListOutput{PullRequests: make([]mcpPR, 0, len(list))}
	for _, p := range list {
		out.PullRequests = append(out.PullRequests, toMCPPR(p))
	}
	text := fmt.Sprintf("%d %s tracked.", len(out.PullRequests), plural(len(out.PullRequests), "pull request", "pull requests"))
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, out, nil
}

func toMCPPR(p api.PRStatus) mcpPR {
	m := mcpPR{
		Number:       p.Number,
		Title:        p.Title,
		Author:       p.Author,
		State:        prStateWord(p),
		RenderStatus: string(p.Status),
		URL:          p.URL,
		Draft:        p.Draft,
		Hidden:       p.Hidden,
	}
	if p.Signals != nil {
		m.ResourceChanges = p.Signals.Resources
		m.Cautions = p.Signals.Caution
		m.ImageChanges = p.Signals.Images
		m.RenderFailures = p.Signals.Failures
	}
	if p.Checks != nil {
		m.CIChecks = string(p.Checks.State)
	}
	return m
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
		// A tool-level error the model sees and can recover from (wrong number),
		// not a transport error.
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("No pull request #%d is tracked.", in.Number)}},
		}, nil, nil
	}
	// The same Markdown summary konflate posts as a PR comment — GitHub-flavoured
	// admonitions render in a Markdown-aware agent and degrade to plain text.
	md := summaryMarkdownBody(env, s.reviewURL(in.Number), true)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: md}}}, nil, nil
}
