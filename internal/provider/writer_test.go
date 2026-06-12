package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	forgejo "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/golang-jwt/jwt/v4"
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/google/go-github/v88/github"

	"github.com/home-operations/konflate/internal/api"
	"github.com/home-operations/konflate/internal/config"
)

// testRSAKeyPEM returns a freshly-generated PKCS#1 RSA private key in PEM form —
// a syntactically valid KONFLATE_APP_PRIVATE_KEY for exercising the App path.
func testRSAKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}

func TestGitHubWriter_SetStatus(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	var got struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/web/statuses/"+sha, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	raw := srv.URL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	wr := &githubWriter{client: client, owner: "acme", repo: "web"}

	err = wr.SetStatus(context.Background(), api.PR{Number: 7, HeadSHA: sha}, Status{
		State: StatusSuccess, Description: "rendered", TargetURL: "https://k.example/#/pr/7", Context: "konflate",
	})
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got.State != "success" || got.Context != "konflate" ||
		got.TargetURL != "https://k.example/#/pr/7" || got.Description != "rendered" {
		t.Errorf("status payload = %+v", got)
	}
}

func TestNewWriter_NilWhenDisabled(t *testing.T) {
	t.Parallel()
	w, err := NewWriter(&config.Config{}) // no write credential
	if err != nil || w != nil {
		t.Fatalf("NewWriter with no credential = (%v, %v); want (nil, nil)", w, err)
	}
}

// TestClientIDSigner_SetsIssuer is the crux of GitHub App auth: ghinstallation
// builds the JWT with the numeric app id as the issuer, and the signer must
// rewrite it to the App's client id (which is what konflate is configured with).
func TestClientIDSigner_SetsIssuer(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const clientID = "Iv23liExAmple"
	s := clientIDSigner{clientID: clientID, inner: ghinstallation.NewRSASigner(jwt.SigningMethodRS256, key)}

	tok, err := s.Sign(&jwt.RegisteredClaims{Issuer: "0"}) // 0 = the placeholder app id we pass ghinstallation
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	var got jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(tok, &got); err != nil {
		t.Fatalf("parse signed token: %v", err)
	}
	if got.Issuer != clientID {
		t.Errorf("issuer = %q, want %q", got.Issuer, clientID)
	}
}

// TestNewGitHubWriteClient covers credential selection: a complete App config
// wins, a write PAT is the fallback, a partial App config is a clear error, and
// no credential is an error.
func TestNewGitHubWriteClient(t *testing.T) {
	t.Parallel()
	pemKey := testRSAKeyPEM(t)
	gh := config.ForgeURI{Kind: config.ForgeGitHub, RepoPath: "acme/web"}
	cases := []struct {
		name    string
		cfg     *config.Config
		wantErr string // substring; "" means a client must be returned
	}{
		{"pat", &config.Config{Forge: gh, WriteToken: "tok"}, ""},
		{"complete app", &config.Config{Forge: gh, AppClientID: "Iv1", AppPrivateKey: pemKey, AppInstallationID: 42}, ""},
		{"app wins over pat", &config.Config{Forge: gh, WriteToken: "tok", AppClientID: "Iv1", AppPrivateKey: pemKey, AppInstallationID: 42}, ""},
		{"app missing installation id", &config.Config{Forge: gh, AppClientID: "Iv1", AppPrivateKey: pemKey}, "KONFLATE_APP_INSTALLATION_ID"},
		{"app malformed key", &config.Config{Forge: gh, AppClientID: "Iv1", AppPrivateKey: "not-a-pem", AppInstallationID: 42}, "private key"},
		{"no credential", &config.Config{Forge: gh}, "write-back needs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, err := newGitHubWriteClient(tc.cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("newGitHubWriteClient: unexpected error %v", err)
				}
				if client == nil {
					t.Fatal("newGitHubWriteClient returned a nil client")
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// statusCapture is an httptest handler that records the POST body and path of a
// commit-status write while answering anything else (e.g. an SDK version probe)
// with an empty JSON object, so the Forgejo/GitLab clients construct and call
// cleanly without a live forge.
func statusCapture(body any, path *string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/statuses/") {
			*path = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(body)
			_, _ = w.Write([]byte(`{"id":1}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}
}

func TestForgejoWriter_SetStatus(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	var got struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	var gotPath string
	srv := httptest.NewServer(statusCapture(&got, &gotPath))
	t.Cleanup(srv.Close)

	client, err := forgejo.NewClient(srv.URL,
		forgejo.SetForgejoVersion(forgejo.Version()), forgejo.SetToken("tok"))
	if err != nil {
		t.Fatalf("forgejo.NewClient: %v", err)
	}
	wr := &forgejoWriter{client: client, owner: "acme", repo: "web"}

	err = wr.SetStatus(context.Background(), api.PR{Number: 7, HeadSHA: sha}, Status{
		State: StatusFailure, Description: "render failed", TargetURL: "https://k.example/#/pr/7", Context: "konflate",
	})
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/repos/acme/web/statuses/"+sha) {
		t.Errorf("path = %q, want suffix /repos/acme/web/statuses/%s", gotPath, sha)
	}
	if got.State != "failure" || got.Context != "konflate" ||
		got.TargetURL != "https://k.example/#/pr/7" || got.Description != "render failed" {
		t.Errorf("status payload = %+v", got)
	}
}

func TestGitlabWriter_SetStatus(t *testing.T) {
	t.Parallel()
	const sha = "deadbeefcafe"
	var got struct {
		State       string `json:"state"`
		Name        string `json:"name"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	var gotPath string
	srv := httptest.NewServer(statusCapture(&got, &gotPath))
	t.Cleanup(srv.Close)

	client, err := gitlab.NewClient("tok", gitlab.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("gitlab.NewClient: %v", err)
	}
	wr := &gitlabWriter{client: client, project: "acme/web"}

	err = wr.SetStatus(context.Background(), api.PR{Number: 7, HeadSHA: sha}, Status{
		State: StatusSuccess, Description: "rendered", TargetURL: "https://k.example/#/pr/7", Context: "konflate",
	})
	if err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if !strings.Contains(gotPath, "/statuses/"+sha) {
		t.Errorf("path = %q, want to contain /statuses/%s", gotPath, sha)
	}
	// GitLab surfaces the status under its "name", and uses "success" for ok.
	if got.State != "success" || got.Name != "konflate" ||
		got.TargetURL != "https://k.example/#/pr/7" || got.Description != "rendered" {
		t.Errorf("status payload = %+v", got)
	}
}

func TestGitlabBuildState(t *testing.T) {
	t.Parallel()
	cases := map[string]gitlab.BuildStateValue{
		StatusSuccess: gitlab.Success,
		StatusFailure: gitlab.Failed, // GitLab uses "failed", not "failure"
		StatusPending: gitlab.Pending,
		"anything":    gitlab.Pending, // unknown maps to pending
	}
	for in, want := range cases {
		if got := gitlabBuildState(in); got != want {
			t.Errorf("gitlabBuildState(%q) = %q, want %q", in, got, want)
		}
	}
}
