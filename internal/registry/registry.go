// Package registry checks whether container image references exist upstream, so
// konflate can flag a PR that points at a tag or digest the registry doesn't
// have (a typo, or an image that was never published) before it ImagePullBackOffs
// in-cluster. It speaks the OCI distribution API via go-containerregistry, which
// handles each registry's auth/token flow (ghcr, Docker Hub, quay, …) and, for
// private registries, the operator-provided docker config (DOCKER_CONFIG /
// a mounted dockerconfigjson) — never a manifest's imagePullSecrets, which
// konflate can't resolve (it renders YAML, holding no cluster/decryption state).
package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// defaultTTL caches each definitive existence result briefly so repeated renders
// of the same PR don't re-dial. Short enough that a tag pushed just after a PR
// opened it (a not-yet-built image) is re-checked on the next render/refresh.
const defaultTTL = 15 * time.Minute

// Client resolves image references against their registries, caching each
// definitive result for a short TTL. Safe for concurrent use.
type Client struct {
	// head performs the registry HEAD; a field so tests can inject a fake without
	// touching the network. Production uses the ambient docker keychain.
	head func(ctx context.Context, ref name.Reference) error
	ttl  time.Duration
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	found bool
	exp   time.Time
}

// New returns a Client that authenticates with the ambient docker keychain
// (DOCKER_CONFIG / a mounted dockerconfigjson), so public registries need no
// credentials and private ones work when the operator provides them. The
// keychain is host-scoped — credentials for one registry are never sent to
// another — so a private-registry token can't leak to an unrelated host.
func New() *Client {
	kc := authn.DefaultKeychain
	return &Client{
		head: func(ctx context.Context, ref name.Reference) error {
			_, err := remote.Head(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
			return err
		},
		ttl:   defaultTTL,
		now:   time.Now,
		cache: map[string]entry{},
	}
}

// Exists reports whether ref resolves in its registry:
//   - (true, nil)  present.
//   - (false, nil) definitively absent — the registry answered 404 / manifest unknown.
//   - (_, err)     indeterminate: the ref is unparseable, or the registry needs
//     auth we don't have, or the request failed. The caller must NOT treat an
//     indeterminate result as missing — that would be a false "not found".
func (c *Client) Exists(ctx context.Context, ref string) (bool, error) {
	if found, ok := c.cached(ref); ok {
		return found, nil
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return false, fmt.Errorf("parse reference %q: %w", ref, err)
	}
	switch err := c.head(ctx, parsed); {
	case err == nil:
		c.store(ref, true)
		return true, nil
	case isNotFound(err):
		c.store(ref, false)
		return false, nil
	default:
		return false, err // indeterminate — not cached, so it's retried next render
	}
}

// isNotFound reports whether err is the registry definitively saying the
// tag/digest is absent (HTTP 404), as opposed to an auth or transport failure.
func isNotFound(err error) bool {
	var te *transport.Error
	return errors.As(err, &te) && te.StatusCode == http.StatusNotFound
}

func (c *Client) cached(ref string) (found, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[ref]
	if !ok || c.now().After(e.exp) {
		return false, false
	}
	return e.found, true
}

func (c *Client) store(ref string, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[ref] = entry{found: found, exp: c.now().Add(c.ttl)}
}
