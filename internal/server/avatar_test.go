package server

import "testing"

// TestAvatarDialControl is the SSRF guard on the avatar proxy: it must refuse to
// dial non-public addresses (loopback, RFC1918/ULA private, link-local incl. the
// cloud-metadata IP, multicast, unspecified — including IPv4-mapped IPv6) and
// allow public ones. The hook runs on every dial and redirect hop, so this is the
// last line of defense against a forge-returned avatar URL reaching the cluster.
func TestAvatarDialControl(t *testing.T) {
	t.Parallel()
	blocked := []string{
		"127.0.0.1:443",                // loopback
		"10.0.0.1:443",                 // RFC1918
		"172.16.5.4:443",               // RFC1918
		"192.168.1.1:443",              // RFC1918
		"169.254.169.254:443",          // link-local / cloud metadata
		"[::1]:443",                    // IPv6 loopback
		"[fd00::1]:443",                // IPv6 unique-local (private)
		"[fe80::1]:443",                // IPv6 link-local
		"[::ffff:169.254.169.254]:443", // IPv4-mapped metadata — guards the Unmap()
		"224.0.0.1:443",                // multicast
		"0.0.0.0:443",                  // unspecified
	}
	for _, addr := range blocked {
		if err := avatarDialControl("tcp", addr, nil); err == nil {
			t.Errorf("avatarDialControl(%q) = nil, want a refusal", addr)
		}
	}
	allowed := []string{
		"93.184.216.34:443",                        // public IPv4
		"[2606:2800:220:1:248:1893:25c8:1946]:443", // public IPv6
	}
	for _, addr := range allowed {
		if err := avatarDialControl("tcp", addr, nil); err != nil {
			t.Errorf("avatarDialControl(%q) = %v, want nil (public)", addr, err)
		}
	}
}
