package fetch

import (
	"errors"
	"net/netip"
	"strings"
	"syscall"
	"testing"
)

// Everything in this file is pure: no DNS, no dial, no httptest. That is the
// point of testing the guard separately from Fetch — newDialControl in
// particular is the anti-rebinding gate, and calling it directly is what lets
// a "resolves public, connects private" case be pinned without needing a
// resolver that lies. fetch_test.go covers the same guard reached through the
// real transport.

func TestValidateURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		rawURL   string
		wantErr  string // substring; "" means the URL must be accepted
		wantHost string
	}{
		{name: "http is accepted", rawURL: "http://example.com/doc.pdf", wantHost: "example.com"},
		{name: "https is accepted", rawURL: "https://example.com/doc.pdf", wantHost: "example.com"},
		{
			name:     "a port is not rejected here",
			rawURL:   "http://example.com:631/doc.pdf",
			wantHost: "example.com",
			// Port policy lives in portAllowed, not validateURL, because it
			// depends on allowPrivate. Pinning acceptance here keeps that
			// separation from being quietly collapsed back together.
		},
		{name: "ftp is rejected", rawURL: "ftp://example.com/doc.pdf", wantErr: `scheme must be http or https, got "ftp"`},
		{name: "file is rejected", rawURL: "file:///etc/passwd", wantErr: `scheme must be http or https, got "file"`},
		{name: "a scheme-less URL is rejected", rawURL: "example.com/doc.pdf", wantErr: `scheme must be http or https, got ""`},
		{
			name:   "an unparseable URL is rejected",
			rawURL: "http://exa mple.com/doc.pdf",
			// A literal space is one of the few inputs url.Parse itself
			// rejects; most malformed-looking URLs parse fine and are caught
			// by the scheme/host checks instead.
			wantErr: "invalid file_url",
		},
		{
			name:   "embedded credentials are rejected",
			rawURL: "http://user:pass@example.com/doc.pdf",
			// Credentials in file_url are an SSRF-adjacent smell (they let a
			// caller borrow the gateway's network position with someone
			// else's auth) and are rejected outright rather than stripped.
			wantErr: "must not contain embedded credentials",
		},
		{name: "a bare username is rejected too", rawURL: "http://user@example.com/doc.pdf", wantErr: "must not contain embedded credentials"},
		{name: "no host is rejected", rawURL: "http:///doc.pdf", wantErr: "must name a host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			u, err := validateURL(tt.rawURL)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateURL(%q) = error %v, want success", tt.rawURL, err)
				}
				if u.Hostname() != tt.wantHost {
					t.Errorf("hostname = %q, want %q", u.Hostname(), tt.wantHost)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateURL(%q) succeeded, want error containing %q", tt.rawURL, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if u != nil {
				t.Errorf("URL = %v on the error path, want nil", u)
			}
		})
	}
}

// TestDefaultPort covers defaultPort directly because its one call site
// cannot distinguish it. Replacing `port = defaultPort(u.Scheme)` in Fetch
// with a hardcoded "80" survives the whole suite, and that mutant is
// genuinely EQUIVALENT today: 80 and 443 are both in allowedPorts, so which
// of the two a port-less URL is checked against changes no outcome, and the
// dial Control re-checks the port actually being connected to regardless.
// It stops being equivalent the moment allowedPorts is narrowed, which is
// why the function is pinned here rather than left to the call site.
func TestDefaultPort(t *testing.T) {
	t.Parallel()

	if got := defaultPort("https"); got != "443" {
		t.Errorf(`defaultPort("https") = %q, want "443"`, got)
	}
	// Anything that is not https defaults to 80; validateURL has already
	// guaranteed the only two schemes that can reach here.
	if got := defaultPort("http"); got != "80" {
		t.Errorf(`defaultPort("http") = %q, want "80"`, got)
	}
}

func TestPortAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		port         string
		allowPrivate bool
		want         bool
	}{
		{name: "80 is allowed", port: "80", want: true},
		{name: "443 is allowed", port: "443", want: true},
		{
			name: "631 is refused",
			port: "631",
			// HLD 11.3's own motivating example: local CUPS admin. If this
			// row ever flips, the port allowlist has stopped doing the one
			// job it was introduced for.
			want: false,
		},
		{name: "22 is refused", port: "22", want: false},
		{name: "8080 is refused", port: "8080", want: false},
		{name: "an empty port is refused", port: "", want: false},
		{
			name: "allowPrivate lifts the port check",
			port: "631", allowPrivate: true, want: true,
			// httptest.Server always binds an ephemeral high port, so the
			// escape hatch has to cover the port check as well as the address
			// block, or fetch's own tests could never reach it. This is also
			// why the README states the flag disables both.
		},
		{name: "allowPrivate allows an ephemeral port", port: "49152", allowPrivate: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := portAllowed(tt.port, tt.allowPrivate); got != tt.want {
				t.Errorf("portAllowed(%q, %v) = %v, want %v", tt.port, tt.allowPrivate, got, tt.want)
			}
		})
	}
}

func TestHostAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		host      string
		allowlist []string
		want      bool
	}{
		{name: "an empty allowlist allows any host", host: "example.com", allowlist: nil, want: true},
		{name: "an exact match is allowed", host: "example.com", allowlist: []string{"example.com"}, want: true},
		{name: "a subdomain is allowed", host: "docs.example.com", allowlist: []string{"example.com"}, want: true},
		{name: "a deep subdomain is allowed", host: "a.b.docs.example.com", allowlist: []string{"example.com"}, want: true},
		{
			name: "a non-boundary suffix is refused",
			host: "evilexample.com", allowlist: []string{"example.com"}, want: false,
			// The whole reason hostAllowed matches on a label boundary
			// (host == suffix || HasSuffix(host, "."+suffix)) rather than a
			// plain HasSuffix. Under plain suffix matching an attacker
			// registers evilexample.com and is inside the allowlist.
		},
		{name: "a different domain is refused", host: "example.org", allowlist: []string{"example.com"}, want: false},
		{name: "a prefix match is refused", host: "example.com.evil.net", allowlist: []string{"example.com"}, want: false},
		{
			name: "an allowlisted domain embedded mid-host is refused",
			host: "docs.example.com.evil.net", allowlist: []string{"example.com"}, want: false,
			// The row above ("a prefix match") has no "." before "example.com",
			// so it cannot tell HasSuffix(host, "."+suffix) apart from a
			// looser Contains(host, "."+suffix) — both agree it's refused.
			// This one has a "." right before "example.com" and would pass
			// under Contains, so it is what actually pins the label-boundary
			// requirement: an attacker who registers evil.net can otherwise
			// serve docs.example.com.evil.net and pass an example.com allowlist.
		},
		{name: "any entry in the list can match", host: "b.example.org", allowlist: []string{"example.com", "example.org"}, want: true},
		{name: "an uppercase host matches a lowercase entry", host: "DOCS.EXAMPLE.COM", allowlist: []string{"example.com"}, want: true},
		{
			name: "an uppercase entry matches a lowercase host",
			host: "docs.example.com", allowlist: []string{"EXAMPLE.COM"}, want: true,
			// config.splitHostList lowercases what it produces, but
			// NewSafeFetcher is exported and takes a plain []string, so
			// hostAllowed lowercases the entries itself. This row is what
			// keeps that defensive lowering from being deleted as redundant.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := hostAllowed(tt.host, tt.allowlist); got != tt.want {
				t.Errorf("hostAllowed(%q, %v) = %v, want %v", tt.host, tt.allowlist, got, tt.want)
			}
		})
	}
}

// TestIsBlockedAddr is the security core of this package. The allowed rows
// matter as much as the blocked ones: a mutant that blocks everything is
// still a "safe" fetcher that can never fetch, and only the allowed rows
// catch it.
func TestIsBlockedAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
		want bool
	}{
		// --- stdlib predicates ---
		{name: "IPv4 loopback", addr: "127.0.0.1", want: true},
		{name: "IPv4 loopback, elsewhere in the slash 8", addr: "127.255.255.254", want: true},
		{name: "IPv6 loopback", addr: "::1", want: true},
		{name: "RFC1918 10", addr: "10.0.0.1", want: true},
		{name: "RFC1918 172.16", addr: "172.16.5.4", want: true},
		{name: "RFC1918 192.168", addr: "192.168.1.1", want: true},
		{name: "IPv6 unique local", addr: "fd00::1", want: true},
		{
			name: "IPv4 link-local, the cloud metadata address", addr: "169.254.169.254", want: true,
			// The single most-exploited SSRF target there is.
		},
		{name: "IPv6 link-local", addr: "fe80::1", want: true},
		{name: "IPv4 multicast", addr: "224.0.0.1", want: true},
		{
			name: "IPv6 link-local multicast", addr: "ff02::1", want: true,
			// Equivalent mutant, recorded rather than left silent: dropping
			// IsLinkLocalMulticast is a no-op here because ff02::1 also
			// satisfies IsMulticast (which isBlockedAddr checks separately),
			// so this row cannot tell the two predicates apart. Kept for
			// documentation value — deleting it would look like coverage
			// loss to a future diff, when in fact nothing is lost.
		},
		{
			name: "IPv6 interface-local multicast", addr: "ff01::1", want: true,
			// Same equivalence as the row above, against IsMulticast.
		},
		{
			name: "IPv4 unspecified", addr: "0.0.0.0", want: true,
			// Not actually pinning IsUnspecified: 0.0.0.0 is also matched by
			// the "this-network" 0.0.0.0/8 blockedPrefixes entry below, so
			// this row cannot tell the two predicates apart. Kept under
			// "stdlib predicates" for readability (it's the canonical
			// unspecified address), but dropping IsUnspecified from
			// isBlockedAddr still leaves this row passing via the prefix.
		},
		{
			name: "IPv6 unspecified", addr: "::", want: true,
			// Same story as 0.0.0.0 above: also matched by ::/96.
		},
		{
			name: "IPv4 broadcast", addr: "255.255.255.255", want: true,
			// Also matched by 240.0.0.0/4 — the explicit broadcastAddr
			// comparison in isBlockedAddr is redundant with that prefix and
			// this row cannot tell them apart.
		},

		// --- blockedPrefixes: IPv4 ranges the stdlib predicates miss ---
		{name: "this-network 0", addr: "0.1.2.3", want: true},
		{
			name: "this-network, deep inside the slash 8", addr: "0.200.1.1", want: true,
			// Without this, a mutant narrowing 0.0.0.0/8 to e.g. /9 or /10
			// still blocks 0.1.2.3 above and ships the upper half of the
			// range open — the same class of gap the "deep inside" rows
			// further down already cover for the other prefixes.
		},
		{name: "CGNAT", addr: "100.64.0.1", want: true},
		{name: "CGNAT upper bound", addr: "100.127.255.255", want: true},
		{name: "IETF protocol assignments 192.0.0", addr: "192.0.0.8", want: true},
		{name: "benchmarking 198.18", addr: "198.19.255.255", want: true},
		{name: "reserved 240", addr: "240.0.0.1", want: true},

		// --- blockedPrefixes: IPv6 schemes that embed an IPv4 address ---
		// Each of these would otherwise carry an IPv4-blocked target straight
		// past every IPv6-shaped stdlib predicate, none of which look inside
		// the embedding.
		{name: "IPv4-compatible IPv6 of loopback", addr: "::127.0.0.1", want: true},
		{name: "NAT64 well-known prefix", addr: "64:ff9b::7f00:1", want: true},
		{name: "NAT64 local-use prefix", addr: "64:ff9b:1::1", want: true},
		{name: "6to4", addr: "2002:7f00:1::1", want: true},
		{name: "Teredo", addr: "2001:0:1::1", want: true},
		{name: "discard-only", addr: "100::1", want: true},

		// --- IPv4-mapped IPv6: the Unmap path ---
		{
			name: "IPv4-mapped loopback", addr: "::ffff:127.0.0.1", want: true,
			// IsLoopback unmaps internally, so this row alone does not prove
			// the Unmap call is needed — the next one does.
		},
		{
			name: "IPv4-mapped CGNAT", addr: "::ffff:100.64.0.1", want: true,
			// This is the row that pins Unmap: netip.Prefix.Contains returns
			// false across address families, so without Unmap this walks past
			// the IPv4-only 100.64.0.0/10 entry.
		},
		{name: "IPv4-mapped reserved 240", addr: "::ffff:240.0.0.1", want: true},

		// --- zoned addresses: the 08778a0 regression ---
		// netip.Prefix.Contains documents that it returns false for ANY zoned
		// address, because prefixes carry no zone. Before the WithZone("")
		// call, appending a zone was a complete bypass of blockedPrefixes.
		{name: "zoned IPv4-compatible loopback", addr: "::127.0.0.1%eth0", want: true},
		{name: "zoned 6to4", addr: "2002:7f00:1::1%eth0", want: true},
		{
			name: "zoned IPv6 loopback", addr: "::1%eth0", want: true,
			// Caught by IsLoopback rather than by a prefix, so this row would
			// pass even without the zone fix; it is here to document that a
			// zone is not what makes an address safe.
		},

		// --- allowed: public addresses the gateway must still be able to reach ---
		{name: "public IPv4", addr: "8.8.8.8", want: false},
		{name: "public IPv4, second", addr: "1.1.1.1", want: false},
		{name: "public IPv6", addr: "2606:4700:4700::1111", want: false},
		{name: "IPv4-mapped public", addr: "::ffff:8.8.8.8", want: false},

		// --- allowed: the addresses immediately outside each blocked range ---
		// Without these, an off-by-one widening of any prefix goes unnoticed.
		{name: "just below CGNAT", addr: "100.63.255.255", want: false},
		{name: "just above CGNAT", addr: "100.128.0.0", want: false},
		{name: "just below the 192.0.0 slash 24", addr: "191.255.255.255", want: false},
		{name: "just above the 192.0.0 slash 24", addr: "192.0.1.1", want: false},
		{name: "just below benchmarking", addr: "198.17.255.255", want: false},
		{name: "just above benchmarking", addr: "198.20.0.0", want: false},
		{
			name: "just below reserved 240", addr: "239.255.255.255", want: true,
			// Still inside 224.0.0.0/4, so multicast catches it before the
			// 240/4 prefix would. The next row is the real lower neighbour.
		},
		{name: "just below multicast", addr: "223.255.255.255", want: false},
		{name: "just above this-network", addr: "1.0.0.1", want: false},
		{name: "just above the discard-only slash 64", addr: "100:0:0:1::1", want: false},
		{name: "just above 6to4", addr: "2003::1", want: false},
		{name: "just above the IPv4-compatible slash 96", addr: "::1:0:0", want: false},

		// --- allowed/blocked deep inside each blockedPrefixes range: without
		// these, a boundary-only check leaves the far side of a narrowed
		// range unproven — a mutant that shrinks 240.0.0.0/4 to /5 still
		// blocks the low end but ships wide open at its top.
		{name: "the upper half of the 192.0.0 slash 24", addr: "192.0.0.200", want: true},
		{name: "reserved 240, above the slash 5 midpoint", addr: "250.0.0.1", want: true},
		{name: "NAT64 local-use, high in the slash 48", addr: "64:ff9b:1:ffff::1", want: true},
		{name: "Teredo, high in the slash 32", addr: "2001:0:ffff::1", want: true},
		{name: "discard-only, high in the slash 64", addr: "100::ffff:ffff:ffff:ffff", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip, err := netip.ParseAddr(tt.addr)
			if err != nil {
				t.Fatalf("ParseAddr(%q): %v", tt.addr, err)
			}
			if got := isBlockedAddr(ip); got != tt.want {
				t.Errorf("isBlockedAddr(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// TestIsBlockedAddrFailsClosedOnAnInvalidAddress pins the one branch no
// parseable address can reach: the zero netip.Addr. It is reachable in
// production only if a future caller passes an unchecked value, and the
// contract that matters is that such a value is BLOCKED rather than treated
// as public.
func TestIsBlockedAddrFailsClosedOnAnInvalidAddress(t *testing.T) {
	t.Parallel()

	if !isBlockedAddr(netip.Addr{}) {
		t.Error("isBlockedAddr(zero Addr) = false, want true (fail closed)")
	}
}

// TestNewDialControl exercises the real anti-rebinding gate directly. Control
// runs after DNS resolution, on the literal address about to be connect(2)'d,
// so calling it with an address string is a faithful simulation of a host that
// resolved to that address — no lying resolver required. A target whose DNS
// answer changes between validateURL and dial arrives here exactly like this.
func TestNewDialControl(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		address      string
		allowPrivate bool
		wantErr      string // substring; "" means the dial must be permitted
	}{
		{name: "a public address on 80 is permitted", address: "8.8.8.8:80"},
		{name: "a public address on 443 is permitted", address: "8.8.8.8:443"},
		{name: "a public IPv6 address is permitted", address: "[2606:4700:4700::1111]:443"},
		{
			name: "a rebound loopback address is blocked", address: "127.0.0.1:80",
			wantErr: "127.0.0.1",
			// The DNS-rebinding case: the URL named a public host, the
			// resolver answered loopback, and this is the only check left.
		},
		{name: "a rebound metadata address is blocked", address: "169.254.169.254:80", wantErr: "169.254.169.254"},
		{name: "a rebound private address is blocked", address: "10.1.2.3:443", wantErr: "10.1.2.3"},
		{name: "an IPv6 loopback is blocked", address: "[::1]:80", wantErr: "::1"},
		{
			name: "a public address on a refused port is blocked", address: "8.8.8.8:631",
			wantErr: "port 631 not allowed",
			// Control repeats the port check rather than trusting Fetch's
			// earlier one: this is the address actually being dialed, which
			// is the only thing a post-DNS gate can afford to trust.
		},
		{name: "an unparseable address is blocked", address: "not-an-address", wantErr: "missing port"},
		{
			name: "a hostname instead of an IP is blocked", address: "example.com:80",
			wantErr: "ParseAddr",
			// Control is documented to receive a resolved address. If it ever
			// receives a name, the safe reading is "classification failed",
			// not "assume public".
		},
		{
			name: "allowPrivate permits loopback", address: "127.0.0.1:49152", allowPrivate: true,
			// The escape hatch httptest.Server needs.
		},
		{
			name: "allowPrivate ALSO permits cloud metadata", address: "169.254.169.254:80", allowPrivate: true,
			// Pinned as current behavior, and it is NOT what the plan's own
			// test sketch describes ("loopback allowed, IMDS still blocked").
			// allowPrivate returns nil before any classification runs, here
			// and in Fetch's post-connect trace, so
			// PRINT_GATEWAY_ALLOW_PRIVATE_TARGETS=true opens link-local as
			// well as loopback. Recorded as a production follow-up rather
			// than changed from a test-only drop; if that follow-up is taken,
			// this row flips to wantErr and the change is deliberate rather
			// than a silently broken test.
		},
		{name: "allowPrivate permits an otherwise unparseable address", address: "not-an-address", allowPrivate: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			control := newDialControl(tt.allowPrivate)
			// The syscall.RawConn argument is ignored by the implementation
			// (it exists for socket-option tweaking, which this Control does
			// not do), so a nil is faithful rather than a shortcut.
			err := control("tcp", tt.address, syscall.RawConn(nil))

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("control(%q) = %v, want nil", tt.address, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("control(%q) = nil, want an error containing %q", tt.address, tt.wantErr)
			}
			if !errors.Is(err, errBlockedTarget) {
				// Fetch classifies a 400 off this sentinel; an error that did
				// not wrap it would surface as a generic 502 instead.
				t.Errorf("control(%q) error does not wrap errBlockedTarget: %v", tt.address, err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("control(%q) error = %q, want it to contain %q", tt.address, err, tt.wantErr)
			}
		})
	}
}
