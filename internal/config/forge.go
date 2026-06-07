package config

import (
	"fmt"
	"net/url"
	"strings"
)

// ForgeKind identifies the forge platform.
type ForgeKind string

const (
	ForgeGitHub  ForgeKind = "github"
	ForgeGitLab  ForgeKind = "gitlab"
	ForgeForgejo ForgeKind = "forgejo"
)

// cloudDefaults maps each ForgeKind to its SaaS web host and API base URL.
// These are used when the forge URI omits a host (cloud / SaaS usage).
var cloudDefaults = map[ForgeKind]struct{ web, api string }{
	ForgeGitHub:  {web: "https://github.com", api: "https://api.github.com"},
	ForgeGitLab:  {web: "https://gitlab.com", api: "https://gitlab.com"},
	ForgeForgejo: {web: "https://codeberg.org", api: "https://codeberg.org"},
}

// ForgeURI is the parsed representation of a KONFLATE_REPO forge URI.
//
// A forge URI uses a custom scheme so the forge type, the instance host, and
// the repository path are encoded in one value with no ambiguity:
//
//	scheme://[host]/path
//
//	scheme — forge type: "github", "gitlab", or "forgejo".
//	host   — self-hosted instance (hostname or hostname:port). Omit entirely
//	         for cloud SaaS; the SaaS default is used in that case.
//	path   — repository path: "owner/repo" or "group[/subgroup]/repo".
//
// Examples:
//
//	github://owner/repo                         → GitHub cloud
//	github://ghe.example.com/owner/repo         → GitHub Enterprise Server
//	gitlab://group/repo                         → GitLab cloud (gitlab.com)
//	gitlab://gl.example.com/group/subgroup/repo → self-hosted GitLab
//	forgejo://owner/repo                        → Forgejo cloud (codeberg.org)
//	forgejo://git.example.com/owner/repo        → self-hosted Forgejo
type ForgeURI struct {
	Kind     ForgeKind // "github" | "gitlab" | "forgejo"
	Host     string    // empty = cloud SaaS; "hostname[:port]" = self-hosted
	RepoPath string    // "owner/repo" or "group/subgroup/repo" (no leading slash)
	APIBase  string    // resolved API base URL (no trailing slash)
	WebBase  string    // resolved web base URL used for clone URLs (no trailing slash)
}

// CloneURL returns the HTTPS URL for git-cloning the repository.
func (f ForgeURI) CloneURL() string { return f.WebBase + "/" + f.RepoPath }

// PullHeadRef returns the server-side ref in the BASE repository that points at
// the head commit of pull/merge request number n. The base repo publishes this
// ref for every request — including cross-repo (fork) ones, whose head branch
// lives in the contributor's repo and so is absent from the base repo's
// refs/heads — so fetching it resolves fork and same-repo PRs alike. GitHub and
// Forgejo/Gitea expose refs/pull/<n>/head; GitLab exposes
// refs/merge-requests/<n>/head.
func (f ForgeURI) PullHeadRef(n int) string {
	if f.Kind == ForgeGitLab {
		return fmt.Sprintf("refs/merge-requests/%d/head", n)
	}
	return fmt.Sprintf("refs/pull/%d/head", n)
}

// ParseForgeURI parses raw into a ForgeURI.
//
// Go's url.Parse places everything between // and the first / into the host
// field, so "github://owner/repo" gives host="owner", path="/repo". The
// parser applies [looksLikeHost] to the parsed host to decide whether it is
// a self-hosted instance or the first component of the repository path.
func ParseForgeURI(raw string) (ForgeURI, error) {
	if raw == "" {
		return ForgeURI{}, fmt.Errorf("forge URI: empty value")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return ForgeURI{}, fmt.Errorf("forge URI %q: %w", raw, err)
	}

	kind := ForgeKind(strings.ToLower(u.Scheme))
	defaults, known := cloudDefaults[kind]
	if !known {
		return ForgeURI{}, fmt.Errorf(
			"forge URI %q: unknown scheme %q — must be github, gitlab, or forgejo",
			raw, u.Scheme,
		)
	}

	// url.Parse places the first double-slash segment into u.Host.
	// Decide whether it is a self-hosted hostname or a repo path component.
	var host, repoPath string
	if looksLikeHost(u.Host) {
		host = u.Host
		repoPath = strings.TrimPrefix(u.Path, "/")
	} else {
		// The parsed "host" is actually the first repo path component
		// (e.g. the GitHub org name or GitLab group name).
		repoPath = strings.TrimPrefix(u.Host+u.Path, "/")
	}

	if err := validateRepoPath(repoPath, raw); err != nil {
		return ForgeURI{}, err
	}

	var apiBase, webBase string
	if host == "" {
		apiBase = defaults.api
		webBase = defaults.web
	} else {
		base := "https://" + host
		if kind == ForgeGitHub {
			apiBase = base + "/api/v3"
		} else {
			apiBase = base
		}
		webBase = base
	}

	return ForgeURI{
		Kind:     kind,
		Host:     host,
		RepoPath: repoPath,
		APIBase:  strings.TrimRight(apiBase, "/"),
		WebBase:  strings.TrimRight(webBase, "/"),
	}, nil
}

// looksLikeHost reports whether s is a hostname (self-hosted instance) rather
// than the first component of a repository path. A hostname is identified by:
//
//   - containing a '.' (e.g. "ghe.example.com", "192.168.1.1")
//   - containing a ':' (e.g. "localhost:3000", an IPv6 literal)
//   - being exactly "localhost" (common for local dev, no port)
//
// Bare words without any of these markers (e.g. "owner", "group") are treated
// as the start of the repository path component.
func looksLikeHost(s string) bool {
	if s == "" {
		return false
	}
	return s == "localhost" || strings.ContainsAny(s, ".:")
}

// validateRepoPath checks that path is a non-empty, slash-containing repo
// path with no leading or trailing slashes.
func validateRepoPath(path, raw string) error {
	if path == "" {
		return fmt.Errorf("forge URI %q: missing repository path", raw)
	}
	if !strings.Contains(path, "/") {
		return fmt.Errorf(
			"forge URI %q: repository path %q must be at least owner/repo",
			raw, path,
		)
	}
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf(
			"forge URI %q: repository path %q must not have leading or trailing slashes",
			raw, path,
		)
	}
	return nil
}
