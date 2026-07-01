package registry

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestClientExists(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		headErr   error
		wantFound bool
		wantErr   bool
	}{
		{"present", nil, true, false},
		{"absent is a definitive 404", &transport.Error{StatusCode: http.StatusNotFound}, false, false},
		{"auth required is indeterminate", &transport.Error{StatusCode: http.StatusUnauthorized}, false, true},
		{"network failure is indeterminate", errors.New("dial tcp: i/o timeout"), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newTestClient(func(context.Context, name.Reference) error { return tc.headErr })
			found, err := c.Exists(context.Background(), "ghcr.io/x/y:1.0")
			if found != tc.wantFound || (err != nil) != tc.wantErr {
				t.Fatalf("Exists = (%v, %v), want found=%v err=%v", found, err, tc.wantFound, tc.wantErr)
			}
		})
	}
}

func TestClientCachesDefinitiveResults(t *testing.T) {
	t.Parallel()
	var calls int
	now := time.Unix(0, 0)
	c := newTestClient(func(context.Context, name.Reference) error { calls++; return nil })
	c.now = func() time.Time { return now }

	for range 3 {
		if found, err := c.Exists(context.Background(), "ghcr.io/x/y:1.0"); !found || err != nil {
			t.Fatalf("Exists = (%v, %v)", found, err)
		}
	}
	if calls != 1 {
		t.Errorf("a cached result should dial once, dialed %d times", calls)
	}
	// Past the TTL it re-dials.
	now = now.Add(defaultTTL + time.Second)
	if _, err := c.Exists(context.Background(), "ghcr.io/x/y:1.0"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("an expired entry should re-dial, calls=%d", calls)
	}
}

func TestClientDoesNotCacheIndeterminate(t *testing.T) {
	t.Parallel()
	var calls int
	c := newTestClient(func(context.Context, name.Reference) error { calls++; return errors.New("boom") })
	_, _ = c.Exists(context.Background(), "ghcr.io/x/y:1.0")
	_, _ = c.Exists(context.Background(), "ghcr.io/x/y:1.0")
	if calls != 2 {
		t.Errorf("indeterminate results must not be cached; dialed %d times, want 2", calls)
	}
}

func TestClientUnparseableRefIsError(t *testing.T) {
	t.Parallel()
	c := newTestClient(func(context.Context, name.Reference) error {
		t.Fatal("head must not be called on an unparseable ref")
		return nil
	})
	if _, err := c.Exists(context.Background(), "BAD"); err == nil {
		t.Error("expected an error for an unparseable reference")
	}
}

func newTestClient(head func(context.Context, name.Reference) error) *Client {
	return &Client{head: head, ttl: defaultTTL, now: time.Now, cache: map[string]entry{}}
}
