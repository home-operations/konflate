package api

import "time"

// PR is the forge-agnostic view of a pull/merge request, served by
// GET /api/prs (list) and GET /api/prs/{number}.
type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	// AuthorAvatar is the forge avatar URL. The server rewrites it to a signed,
	// same-origin /api/avatar path before serving, so the browser never contacts
	// the forge directly and the CSP stays locked to 'self'.
	AuthorAvatar string `json:"authorAvatar,omitempty"`
	// CreatedAt is when the PR was opened on the forge — distinct from the store's
	// last-render time carried by PRStatus.UpdatedAt.
	CreatedAt time.Time `json:"createdAt,omitzero"`
	State     string    `json:"state"`            // raw forge state ("open"/"opened"/"closed"/"merged")
	Open      bool      `json:"open"`             // normalized: still an open PR (forge state strings differ)
	Merged    bool      `json:"merged,omitempty"` // closed via merge (vs abandoned)
	Draft     bool      `json:"draft"`
	HeadRef   string    `json:"headRef"` // PR head branch
	HeadSHA   string    `json:"headSha"` // head commit SHA
	BaseRef   string    `json:"baseRef"` // target branch
	// Fork is true when the head is in a different repository than the base (a
	// cross-repo / fork PR) — an untrusted external contribution. The PR filter
	// (KONFLATE_PR_FILTER_EXPR, default "!pr.fork") excludes these unless an
	// operator's expression admits them; exposed to that filter as pr.fork.
	Fork   bool    `json:"fork"`
	Labels []Label `json:"labels"`
	URL    string  `json:"url"`
}

// Label is a forge label with its display color (a hex string without '#'; may
// be empty when the forge doesn't supply one on the PR list, e.g. GitLab).
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}
