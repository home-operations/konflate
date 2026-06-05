package api

// PR is the forge-agnostic view of a pull/merge request, served by
// GET /api/prs (list) and GET /api/prs/{number}.
type PR struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Author  string   `json:"author"`
	State   string   `json:"state"`            // raw forge state ("open"/"opened"/"closed"/"merged")
	Open    bool     `json:"open"`             // normalized: still an open PR (forge state strings differ)
	Merged  bool     `json:"merged,omitempty"` // closed via merge (vs abandoned)
	Draft   bool     `json:"draft"`
	HeadRef string   `json:"headRef"` // PR head branch
	HeadSHA string   `json:"headSha"` // head commit SHA
	BaseRef string   `json:"baseRef"` // target branch
	Labels  []string `json:"labels"`
	URL     string   `json:"url"`
}
