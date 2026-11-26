package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Errors of a request a Guard refused.
var (
	// ErrBlockedAddress is a connection refused because of the address it
	// would reach, after name resolution.
	ErrBlockedAddress = errors.New("refused to connect to a link-local, loopback, private or cloud metadata address")
	// ErrNotHTTPS is a plain http request to a host not listed as internal.
	ErrNotHTTPS = errors.New("refused a plain http request to a host not listed as internal")
	// ErrRedirect is a redirect the guarded client did not follow.
	ErrRedirect = errors.New("refused to follow a redirect")
	// ErrBodyTooLarge is a body read past the cap LimitBody set.
	ErrBodyTooLarge = errors.New("the answer is larger than allowed")
)

// metadataHosts are the cloud metadata services' names.
var metadataHosts = []string{"metadata", "metadata.google.internal", "metadata.goog", "instance-data", "instance-data.ec2.internal"}

// metadataAddrs are cloud metadata addresses outside the link-local ranges.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
	netip.MustParseAddr("168.63.129.16"),   // Azure platform
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
}

// privateRanges are private ranges netip.Addr.IsPrivate leaves out.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("0.0.0.0/8"),     // this network
}

// MetadataHost reports whether host names a cloud metadata service,
// ignoring case and a trailing dot.
func MetadataHost(host string) bool {
	return slices.Contains(metadataHosts, strings.TrimSuffix(strings.ToLower(host), "."))
}

// Blocked is a link-local, unspecified or metadata address: no guard ever
// connects to one.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || slices.Contains(metadataAddrs, ip)
}

// private is a loopback, multicast or private address (RFC 1918, ULA,
// carrier-grade NAT).
func private(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsMulticast() || ip.IsPrivate() ||
		slices.ContainsFunc(privateRanges, func(p netip.Prefix) bool { return p.Contains(ip) })
}

// Guard keeps outbound requests to addresses an admin typed (or a server
// they named advertised) away from what they must not reach. Blocked
// addresses are always refused. The address checked is the one actually
// dialled, after name resolution, so a name cannot point past the guard.
type Guard struct {
	// PublicOnly also refuses loopback, multicast and private addresses
	// and plain http, except for hosts in Internal. Its client sends no
	// request through a proxy, whose address would hide the one reached.
	PublicOnly bool
	// Internal are host names (as in the URL, no port; case ignored) that
	// may resolve to private addresses and be spoken to over plain http.
	Internal []string
}

func (g Guard) internal(host string) bool {
	return slices.ContainsFunc(g.Internal, func(h string) bool { return strings.EqualFold(h, host) })
}

// Transport is the default transport whose connections pass the guard.
func (g Guard) Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if g.PublicOnly {
		t.Proxy = nil
	}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, ErrBlockedAddress
		}
		allowPrivate := !g.PublicOnly || g.internal(host)
		d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlockedAddress
			}
			ip, err := netip.ParseAddr(h)
			if err != nil || Blocked(ip) || (!allowPrivate && private(ip)) {
				return ErrBlockedAddress
			}
			return nil
		}}
		return d.DialContext(ctx, network, address)
	}
	return t
}

// Client is an HTTP client over Transport that follows no redirect and,
// when PublicOnly, sends plain http only to Internal hosts.
func (g Guard) Client() *http.Client {
	return &http.Client{
		Transport:     schemeGuard{g: g, next: g.Transport()},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}
}

type schemeGuard struct {
	g    Guard
	next http.RoundTripper
}

func (s schemeGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	if s.g.PublicOnly && r.URL.Scheme != "https" && !s.g.internal(r.URL.Hostname()) {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, ErrNotHTTPS
	}
	return s.next.RoundTrip(r)
}

// LimitBody is body that fails with ErrBodyTooLarge once more than n bytes
// are read, so a caller never takes a cut answer for a whole one.
func LimitBody(body io.ReadCloser, n int64) io.ReadCloser {
	return &limited{rc: body, left: n}
}

type limited struct {
	rc   io.ReadCloser
	left int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, ErrBodyTooLarge
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.rc.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n + int(l.left), ErrBodyTooLarge
	}
	return n, err
}

func (l *limited) Close() error { return l.rc.Close() }
