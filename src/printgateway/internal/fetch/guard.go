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

// allowedPorts is not a config knob: a bad env value could widen it and
// reopen the 127.0.0.1:631 (CUPS admin port) attack it exists to block.
var allowedPorts = map[string]bool{"80": true, "443": true}

// errBlockedTarget and errRedirectNotAllowed are sentinels so Fetch can tell
// a policy rejection (400) apart from a genuine network failure (502)
// without string-matching an error message.
var (
	errBlockedTarget      = errors.New("file_url resolved to a disallowed address")
	errRedirectNotAllowed = errors.New("file_url must be a direct link: redirects are not followed")
)

// validateURL checks the parts of rawURL knowable before any DNS lookup or
// dial: scheme, embedded credentials, and that a host is named. Port policy
// lives in portAllowed instead, since it depends on allowPrivate.
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

// portAllowed applies the port-80/443 policy; allowPrivate also lifts it,
// since httptest.Server binds an ephemeral high port.
func portAllowed(port string, allowPrivate bool) bool {
	return allowPrivate || allowedPorts[port]
}

// hostAllowed enforces the optional host-suffix allowlist of pre-approved
// sources. An empty allowlist means "any public host" — the
// private/loopback/link-local block below still applies regardless.
func hostAllowed(host string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}
	host = strings.ToLower(host)
	for _, suffix := range allowlist {
		// Lower here too rather than rely on config.splitHostList's own
		// normalization: NewSafeFetcher is exported and takes a plain []string.
		suffix = strings.ToLower(suffix)
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// blockedPrefixes covers ranges netip.Addr's own IsPrivate/IsLoopback/
// IsLinkLocalUnicast/IsMulticast/IsUnspecified miss: CGNAT/IANA special-use
// IPv4 ranges, plus IPv6 schemes that embed an IPv4 address (which the
// IPv6-shaped stdlib predicates don't look inside).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved

	netip.MustParsePrefix("::/96"),          // IPv4-compatible IPv6, e.g. ::127.0.0.1
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use prefix
	netip.MustParsePrefix("2002::/16"),      // 6to4
	netip.MustParsePrefix("2001::/32"),      // Teredo — also embeds an IPv4 address
	netip.MustParsePrefix("100::/64"),       // discard-only
}

var broadcastAddr = netip.MustParseAddr("255.255.255.255")

// isBlockedAddr is the anti-SSRF address check itself.
func isBlockedAddr(ip netip.Addr) bool {
	// Unmap: netip.Prefix.Contains returns false across address families, so
	// an IPv4-mapped IPv6 literal would otherwise skip IPv4-only prefixes
	// like 100.64.0.0/10.
	ip = ip.Unmap()
	// Strip any zone: netip.Prefix.Contains always returns false for a zoned
	// address, so e.g. "%eth0" would otherwise bypass every blockedPrefixes entry.
	ip = ip.WithZone("")
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
// anti-rebinding gate: it runs after DNS resolution, on the literal IP about
// to be connect(2)'d, catching a target that resolved public at validateURL
// time but private by dial time.
func newDialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}

		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: %v", errBlockedTarget, err)
		}
		// allowedPorts directly, not portAllowed: allowPrivate already
		// returned above, so this line is always the production check.
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
