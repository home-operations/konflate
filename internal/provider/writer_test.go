package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
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

	wr := &githubWriter{client: newGitHubTestClient(t, srv.URL), owner: "acme", repo: "web"}

	err := wr.SetStatus(context.Background(), api.PR{Number: 7, HeadSHA: sha}, Status{
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

// newGitHubTestClient builds a go-github client pointed at a test server.
func newGitHubTestClient(t *testing.T, baseURL string) *github.Client {
	t.Helper()
	raw := baseURL + "/"
	client, err := github.NewClient(github.WithURLs(&raw, &raw))
	if err != nil {
		t.Fatalf("github.NewClient: %v", err)
	}
	return client
}

// commentSink records what a forge comment API received during a test.
type commentSink struct {
	created, edited bool
	body            string
}

// commentCapture answers the three calls UpsertComment makes against any forge:
// GET lists comments (listJSON), POST creates one, PUT/PATCH edits one — recording
// the create/edit and the body sent. A non-comment GET (e.g. a stray probe) gets
// an empty object so the client still constructs cleanly.
func commentCapture(listJSON string, sink *commentSink) http.HandlerFunc {
	decodeBody := func(r *http.Request) string {
		var c struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&c)
		return c.Body
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if strings.Contains(r.URL.Path, "comment") || strings.Contains(r.URL.Path, "/notes") {
				_, _ = w.Write([]byte(listJSON))
			} else {
				_, _ = w.Write([]byte(`{}`)) // not the comment list (e.g. a probe)
			}
		case http.MethodPost:
			sink.created = true
			sink.body = decodeBody(r)
			_, _ = w.Write([]byte(`{"id":1}`))
		case http.MethodPut, http.MethodPatch:
			sink.edited = true
			sink.body = decodeBody(r)
			_, _ = w.Write([]byte(`{"id":99}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}
}

const upsertMarker = "<!-- konflate:pr-7 -->"

// markerList is a one-comment listing (id 99) whose body carries the marker.
func markerList() string {
	return fmt.Sprintf(`[{"id":99,"body":%q}]`, upsertMarker+"\nold")
}

func assertCreated(t *testing.T, sink commentSink) {
	t.Helper()
	if !sink.created || sink.edited {
		t.Fatalf("want a created comment, got created=%v edited=%v", sink.created, sink.edited)
	}
	if !strings.Contains(sink.body, upsertMarker) {
		t.Errorf("created body missing the marker: %q", sink.body)
	}
}

func assertEdited(t *testing.T, sink commentSink) {
	t.Helper()
	if !sink.edited || sink.created {
		t.Fatalf("want an edited comment, got created=%v edited=%v", sink.created, sink.edited)
	}
	if !strings.Contains(sink.body, "new") {
		t.Errorf("edited body = %q, want it to contain the updated text", sink.body)
	}
}

func TestGitHubWriter_UpsertComment(t *testing.T) {
	t.Parallel()
	t.Run("creates when absent", func(t *testing.T) {
		t.Parallel()
		var sink commentSink
		srv := httptest.NewServer(commentCapture(`[]`, &sink))
		t.Cleanup(srv.Close)
		wr := &githubWriter{client: newGitHubTestClient(t, srv.URL), owner: "acme", repo: "web"}
		if err := wr.UpsertComment(context.Background(), api.PR{Number: 7}, upsertMarker, upsertMarker+"\nhi"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		assertCreated(t, sink)
	})
	t.Run("edits when present", func(t *testing.T) {
		t.Parallel()
		var sink commentSink
		srv := httptest.NewServer(commentCapture(markerList(), &sink))
		t.Cleanup(srv.Close)
		wr := &githubWriter{client: newGitHubTestClient(t, srv.URL), owner: "acme", repo: "web"}
		if err := wr.UpsertComment(context.Background(), api.PR{Number: 7}, upsertMarker, upsertMarker+"\nnew"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		assertEdited(t, sink)
	})
}

func TestGitlabWriter_UpsertComment(t *testing.T) {
	t.Parallel()
	newClient := func(t *testing.T, url string) *gitlab.Client {
		t.Helper()
		c, err := gitlab.NewClient("tok", gitlab.WithBaseURL(url))
		if err != nil {
			t.Fatalf("gitlab.NewClient: %v", err)
		}
		return c
	}
	t.Run("creates when absent", func(t *testing.T) {
		t.Parallel()
		var sink commentSink
		srv := httptest.NewServer(commentCapture(`[]`, &sink))
		t.Cleanup(srv.Close)
		wr := &gitlabWriter{client: newClient(t, srv.URL), project: "acme/web"}
		if err := wr.UpsertComment(context.Background(), api.PR{Number: 7}, upsertMarker, upsertMarker+"\nhi"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		assertCreated(t, sink)
	})
	t.Run("edits when present", func(t *testing.T) {
		t.Parallel()
		var sink commentSink
		srv := httptest.NewServer(commentCapture(markerList(), &sink))
		t.Cleanup(srv.Close)
		wr := &gitlabWriter{client: newClient(t, srv.URL), project: "acme/web"}
		if err := wr.UpsertComment(context.Background(), api.PR{Number: 7}, upsertMarker, upsertMarker+"\nnew"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		assertEdited(t, sink)
	})
}

func TestForgejoWriter_UpsertComment(t *testing.T) {
	t.Parallel()
	newClient := func(t *testing.T, url string) *forgejo.Client {
		t.Helper()
		c, err := forgejo.NewClient(url, forgejo.SetForgejoVersion(forgejo.Version()), forgejo.SetToken("tok"))
		if err != nil {
			t.Fatalf("forgejo.NewClient: %v", err)
		}
		return c
	}
	t.Run("creates when absent", func(t *testing.T) {
		t.Parallel()
		var sink commentSink
		srv := httptest.NewServer(commentCapture(`[]`, &sink))
		t.Cleanup(srv.Close)
		wr := &forgejoWriter{client: newClient(t, srv.URL), owner: "acme", repo: "web"}
		if err := wr.UpsertComment(context.Background(), api.PR{Number: 7}, upsertMarker, upsertMarker+"\nhi"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		assertCreated(t, sink)
	})
	t.Run("edits when present", func(t *testing.T) {
		t.Parallel()
		var sink commentSink
		srv := httptest.NewServer(commentCapture(markerList(), &sink))
		t.Cleanup(srv.Close)
		wr := &forgejoWriter{client: newClient(t, srv.URL), owner: "acme", repo: "web"}
		if err := wr.UpsertComment(context.Background(), api.PR{Number: 7}, upsertMarker, upsertMarker+"\nnew"); err != nil {
			t.Fatalf("UpsertComment: %v", err)
		}
		assertEdited(t, sink)
	})
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

// statusServer answers every request with the given HTTP status and an empty JSON
// body — enough to exercise each writer's Verify (a repo/project GET).
func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRejectedIf(t *testing.T) {
	t.Parallel()
	base := errors.New("boom")
	for _, s := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		if err := rejectedIf(s, base); !errors.Is(err, ErrWriteAuthRejected) || !errors.Is(err, base) {
			t.Errorf("status %d: want ErrWriteAuthRejected wrapping the cause, got %v", s, err)
		}
	}
	for _, s := range []int{0, 200, 429, 500, 503} {
		if err := rejectedIf(s, base); errors.Is(err, ErrWriteAuthRejected) || !errors.Is(err, base) {
			t.Errorf("status %d: want a plain (transient) error, got %v", s, err)
		}
	}
}

// verifyCases is the shared status→result table for every forge's Verify.
var verifyCases = []struct {
	name             string
	status           int
	wantErr, wantRej bool
}{
	{"ok", http.StatusOK, false, false},
	{"not found", http.StatusNotFound, true, true},
	{"forbidden", http.StatusForbidden, true, true},
	{"server error", http.StatusInternalServerError, true, false},
}

func checkVerify(t *testing.T, err error, wantErr, wantRej bool) {
	t.Helper()
	if wantErr != (err != nil) {
		t.Fatalf("err = %v, wantErr = %v", err, wantErr)
	}
	if got := errors.Is(err, ErrWriteAuthRejected); got != wantRej {
		t.Errorf("ErrWriteAuthRejected = %v, want %v (err = %v)", got, wantRej, err)
	}
}

func TestGitHubWriter_Verify(t *testing.T) {
	t.Parallel()
	for _, tc := range verifyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := statusServer(t, tc.status)
			wr := &githubWriter{client: newGitHubTestClient(t, srv.URL), owner: "acme", repo: "web"}
			checkVerify(t, wr.Verify(context.Background()), tc.wantErr, tc.wantRej)
		})
	}
}

func TestGitlabWriter_Verify(t *testing.T) {
	t.Parallel()
	for _, tc := range verifyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := statusServer(t, tc.status)
			client, err := gitlab.NewClient("tok", gitlab.WithBaseURL(srv.URL))
			if err != nil {
				t.Fatalf("gitlab.NewClient: %v", err)
			}
			wr := &gitlabWriter{client: client, project: "acme/web"}
			checkVerify(t, wr.Verify(context.Background()), tc.wantErr, tc.wantRej)
		})
	}
}

func TestForgejoWriter_Verify(t *testing.T) {
	t.Parallel()
	for _, tc := range verifyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := statusServer(t, tc.status)
			client, err := forgejo.NewClient(srv.URL,
				forgejo.SetForgejoVersion(forgejo.Version()), forgejo.SetToken("tok"))
			if err != nil {
				t.Fatalf("forgejo.NewClient: %v", err)
			}
			wr := &forgejoWriter{client: client, owner: "acme", repo: "web"}
			checkVerify(t, wr.Verify(context.Background()), tc.wantErr, tc.wantRej)
		})
	}
}
