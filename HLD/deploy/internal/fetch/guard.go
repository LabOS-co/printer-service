package fetch

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// allowedPorts is deliberately not a config knob: HLD §11.3's own motivating
// example is 127.0.0.1:631 (local CUPS admin), and a port allowlist that a
// bad env value could widen would just relocate that hole, not close it.
var allowedPorts = map[string]bool{"80": true, "443": true}

// errBlockedTarget and errRedirectNotAllowed are sentinels so Fetch can tell
// a policy rejection (400, safe to describe to the caller) apart from a
// genuine network failure (502, detail stays internal-only) without
// string-matching an error message.
var (
	errBlockedTarget      = errors.New("file_url resolved to a disallowed address")
	errRedirectNotAllowed = errors.New("file_url must be a direct link: redirects are not followed")
)

// validateURL checks the parts of rawURL knowable before any DNS lookup or
// dial: scheme, embedded credentials, and that a host is named. Port policy
// is deliberately not here — see portAllowed — because it depends on
// allowPrivate, which this function doesn't take: keeping it pure URL-shape
// validation, with no policy parameter, is what makes it trivially testable
// on its own.
func validateURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid file_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("file_url scheme must be http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("file_url must not contain embedded credentials")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("file_url must name a host")
	}
	return u, nil
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

// portAllowed applies the port-80/443 policy, with the same allowPrivate
// escape hatch as the address block: httptest.Server always binds an
// ephemeral high port, so without this, AllowPrivateTargets would lift the
// address check but still leave fetch's own tests unable to reach it —
// defeating the one escape hatch the plan designed in for exactly this.
func portAllowed(port string, allowPrivate bool) bool {
	return allowPrivate || allowedPorts[port]
}

// hostAllowed enforces the optional host-suffix allowlist (HLD §11.3's
// "pre-approved sources"). An empty allowlist means "any public host" — the
// private/loopback/link-local block below still applies regardless.
func hostAllowed(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	host = strings.ToLower(host)
	for _, suffix := range allowlist {
		// config.splitHostList already lowercases every entry it produces,
		// but NewSafeFetcher is exported and takes a plain []string — lower
		// here too rather than depend on a normalization step in a
		// different package.
		suffix = strings.ToLower(suffix)
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// blockedPrefixes covers ranges netip.Addr's own IsPrivate/IsLoopback/
// IsLinkLocalUnicast/IsMulticast/IsUnspecified don't: CGNAT and other IANA
// special-purpose IPv4 ranges (a cloud metadata endpoint or internal service
// can sit behind any of these, not just RFC1918), plus every IPv6 scheme
// that embeds an IPv4 address — each one would otherwise walk an
// IPv4-blocked address straight past every IPv6-shaped stdlib predicate,
// since none of them know to look inside the embedding.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network" (RFC 1122)
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT (RFC 6598)
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking (RFC 2544)
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved (RFC 1112)

	netip.MustParsePrefix("::/96"),          // IPv4-compatible IPv6 (RFC 4291) — e.g. ::127.0.0.1
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 well-known prefix (RFC 6052)
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use prefix (RFC 8215)
	netip.MustParsePrefix("2002::/16"),      // 6to4 (RFC 3056)
	netip.MustParsePrefix("2001::/32"),      // Teredo (RFC 4380) — also embeds an IPv4 address
	netip.MustParsePrefix("100::/64"),       // discard-only (RFC 6666)
}

var broadcastAddr = netip.MustParseAddr("255.255.255.255")

// isBlockedAddr is the anti-SSRF address check itself (HLD §11.3).
//
// Unmap first: netip.Addr's own IsLoopback/IsPrivate/IsMulticast/
// IsLinkLocalUnicast already unmap internally, so this is NOT what lets
// ::ffff:127.0.0.1 hit IsLoopback — that already works either way.  It
// matters for the blockedPrefixes loop below: netip.Prefix.Contains returns
// false across address families, so an IPv4-mapped IPv6 literal would walk
// straight past an IPv4-only prefix like 100.64.0.0/10 without this.
func isBlockedAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return true // fail closed on anything we can't classify
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	if ip == broadcastAddr {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// newDialControl returns the net.Dialer.Control func that is the real
// anti-rebinding gate: Control runs after DNS resolution, on the literal IP
// about to be connect(2)'d, so a target that resolves to a public IP at
// validateURL time but a private one by dial time (DNS rebinding) is still
// caught. allowPrivate is the escape hatch for tests that must dial
// loopback (httptest.Server) — it is a bool, not a strategy, per the
// project's stance that a Strategy interface with exactly one production
// implementation is unearned complexity.
func newDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}

		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %v", errBlockedTarget, err)
		}
		// allowPrivate already returned above, so this is always the
		// production check — allowedPorts directly, not portAllowed, so a
		// reader doesn't have to chase whether the escape hatch applies here
		// too (it can't reach this line).
		if !allowedPorts[port] {
			return fmt.Errorf("%w: port %s not allowed", errBlockedTarget, port)
		}

		ip, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("%w: %v", errBlockedTarget, err)
		}
		if isBlockedAddr(ip) {
			return fmt.Errorf("%w: %s", errBlockedTarget, ip)
		}
		return nil
	}
}
