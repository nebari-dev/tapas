package httpx_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/httpx"
)

func ok(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/ok", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(s.Close)
	return s
}

// The connectors' guard refuses what an admin-typed address could use to
// reach inside the cluster or the cloud: loopback, private and metadata
// addresses, plain http, and redirects. It checks the address actually
// dialled, so a public name that resolves to a private address is refused
// too.
func TestPublicOnlyGuardRefusesInternalAddresses(t *testing.T) {
	t.Parallel()
	s := ok(t)
	c := httpx.Guard{PublicOnly: true}.Client()
	for name, target := range map[string]string{
		"loopback":    "https://" + strings.TrimPrefix(s.URL, "http://"),
		"private":     "https://10.255.255.1/",
		"cgnat":       "https://100.64.0.1/",
		"ula":         "https://[fd12::1]/",
		"metadata":    "https://169.254.169.254/latest/meta-data/",
		"alibaba":     "https://100.100.100.200/",
		"unspecified": "https://0.0.0.0/",
		"multicast":   "https://224.0.0.1/",
		"mapped":      "https://[::ffff:10.0.0.1]/",
		"nat64":       "https://[64:ff9b::a00:1]/",
		"nat64-local": "https://[64:ff9b:1::a00:1]/",
		"6to4":        "https://[2002:a00:1::1]/",
		"v4-compat":   "https://[::a00:1]/",
		"site-local":  "https://[fec0::1]/",
	} {
		_, err := c.Get(target)
		if !errors.Is(err, httpx.ErrBlockedAddress) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := c.Get("http://example.org/"); !errors.Is(err, httpx.ErrNotHTTPS) {
		t.Errorf("plain http: %v", err)
	}
}

// A host listed as internal may be reached on a private or loopback address
// and over plain http, but still not on a metadata address, and redirects
// are not followed.
func TestInternalHostsMayBePrivate(t *testing.T) {
	t.Parallel()
	s := ok(t)
	u, _ := url.Parse(s.URL)
	c := httpx.Guard{PublicOnly: true, Internal: []string{"127.0.0.1", "LocalHost"}}.Client()
	for _, target := range []string{s.URL + "/", "http://localhost:" + u.Port() + "/"} {
		resp, err := c.Get(target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		resp.Body.Close()
	}
	if _, err := c.Get(s.URL + "/redirect"); !errors.Is(err, httpx.ErrRedirect) {
		t.Errorf("redirect: %v", err)
	}
	meta := httpx.Guard{PublicOnly: true, Internal: []string{"169.254.169.254"}}.Client()
	if _, err := meta.Get("http://169.254.169.254/"); !errors.Is(err, httpx.ErrBlockedAddress) {
		t.Errorf("metadata as internal: %v", err)
	}
}

// The plain guard (the AI settings') refuses only link-local, unspecified
// and metadata addresses, as before: a self-hosted model may live on a
// private address.
func TestPlainGuardRefusesOnlyMetadata(t *testing.T) {
	t.Parallel()
	s := ok(t)
	c := &http.Client{Transport: httpx.Guard{}.Transport()}
	resp, err := c.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, err := c.Get("https://169.254.169.254/"); !errors.Is(err, httpx.ErrBlockedAddress) {
		t.Errorf("metadata: %v", err)
	}
	if !httpx.Blocked(netip.MustParseAddr("fd00:ec2::254")) || httpx.Blocked(netip.MustParseAddr("10.0.0.1")) {
		t.Error("Blocked")
	}
	// IPv6 addresses that carry an IPv4 one (NAT64, 6to4, IPv4-compatible)
	// reach that IPv4 address, so a metadata one inside is blocked too.
	for _, a := range []string{"64:ff9b::a9fe:a9fe", "64:ff9b:1::a9fe:a9fe", "2002:a9fe:a9fe::1", "::a9fe:a9fe", "64:ff9b::6464:64c8"} {
		if !httpx.Blocked(netip.MustParseAddr(a)) {
			t.Errorf("Blocked(%s)", a)
		}
	}
	if httpx.Blocked(netip.MustParseAddr("64:ff9b::a00:1")) || httpx.Blocked(netip.MustParseAddr("2002:a00:1::1")) {
		t.Error("a private IPv4 inside IPv6 is not blocked by the plain guard")
	}
	if !httpx.MetadataHost("Metadata.Google.Internal.") || httpx.MetadataHost("example.org") {
		t.Error("MetadataHost")
	}
}

// LimitBody fails a read past the cap instead of cutting the body short
// silently.
func TestLimitBodyFailsPastTheCap(t *testing.T) {
	t.Parallel()
	b := httpx.LimitBody(io.NopCloser(strings.NewReader("12345")), 4)
	if _, err := io.ReadAll(b); !errors.Is(err, httpx.ErrBodyTooLarge) {
		t.Fatalf("over: %v", err)
	}
	b = httpx.LimitBody(io.NopCloser(strings.NewReader("1234")), 4)
	if got, err := io.ReadAll(b); err != nil || string(got) != "1234" {
		t.Fatalf("at cap: %q %v", got, err)
	}
}
