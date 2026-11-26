package oauth_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/oauth"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	ctx   = context.Background()
	alice = model.Actor{Person: "alice"}
)

const redirectURI = "https://tapas.example.org/settings/connections/callback"

// loopback is the internal host the fakes listen on.
var loopback = []string{"127.0.0.1"}

type fixture struct {
	as   *connectorstest.AuthServer
	mcp  *connectorstest.Connector
	reg  *registry.Registry
	db   *store.Store
	svc  *oauth.Service
	now  time.Time
	conn connectors.Connector
}

// setup starts the fakes and a connector pointing at them. in changes the
// connector before it is created.
func setup(t *testing.T, o connectorstest.AuthServerOptions, in func(*registry.Input)) *fixture {
	t.Helper()
	f := &fixture{as: connectorstest.NewAuthServerWith(t, o), db: storetest.New(t), now: time.Now()}
	f.mcp = connectorstest.NewOAuthConnector(t, f.as)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	f.reg = registry.New(registry.Config{DB: f.db, Seal: key, InternalHosts: loopback})
	input := registry.Input{
		Slug: "fake", Name: "Fake", URL: f.mcp.URL, AuthMode: connectors.AuthUser, Enabled: true,
		ClientID: connectorstest.ClientID, ClientSecret: connectorstest.ClientSecret, Scopes: []string{"read", "write"},
		AllowedGroups: []string{"*"},
	}
	if in != nil {
		in(&input)
	}
	if f.conn, err = f.reg.Create(ctx, alice, input); err != nil {
		t.Fatal(err)
	}
	f.svc = oauth.New(oauth.Config{
		DB: f.db, Seal: key, Clients: f.reg, HTTP: httpx.Guard{}.Client(), InternalHosts: loopback,
		RedirectURI: redirectURI, Now: func() time.Time { return f.now },
	})
	return f
}

// signIn starts a sign-in for person and follows it at the fake vendor.
func (f *fixture) signIn(t *testing.T, person string) (authorize url.Values, back url.Values) {
	t.Helper()
	conn, err := f.reg.Get(ctx, f.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.svc.Start(ctx, model.Actor{Person: person}, conn, person, person)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	back, err = f.as.Authorize(u)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return parsed.Query(), back
}

func completion(back url.Values) oauth.Completion {
	return oauth.Completion{State: back.Get("state"), Code: back.Get("code"), Iss: back.Get("iss")}
}

func TestCodeFlowWithDiscoveryAndAPastedClient(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	q, back := f.signIn(t, "alice")
	for k, want := range map[string]string{
		"response_type": "code", "client_id": connectorstest.ClientID, "redirect_uri": redirectURI,
		"code_challenge_method": "S256", "scope": "read write", "resource": f.mcp.URL,
	} {
		if q.Get(k) != want {
			t.Errorf("authorize %s = %q, want %q", k, q.Get(k), want)
		}
	}
	if raw, err := base64Len(q.Get("state")); err != nil || raw != 32 {
		t.Errorf("state %q is not 32 random bytes", q.Get("state"))
	}
	g, err := f.svc.Complete(ctx, "alice", completion(back))
	if err != nil {
		t.Fatal(err)
	}
	if g.Connector.ID != f.conn.ID || g.Subject != "alice" || g.Tokens.Refresh == "" || g.Tokens.Expiry.IsZero() {
		t.Fatalf("Complete = %+v %+v", g.Connector.ID, g.Subject)
	}
	if !f.as.Valid(g.Tokens.Access, f.mcp.URL) {
		t.Fatal("the access token is not one the vendor issued for the connector")
	}
	if f.as.Registrations() != 0 {
		t.Fatal("registered a client although one was pasted")
	}
}

// Without the hint in WWW-Authenticate, the metadata is found at its
// well-known path.
func TestDiscoveryWithoutTheMetadataHint(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	f.mcp.HideMetadataHint(true)
	_, back := f.signIn(t, "alice")
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); err != nil {
		t.Fatal(err)
	}
}

// With no pasted client, Tapas registers one once and keeps it on the
// connector.
func TestDynamicRegistration(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, func(in *registry.Input) { in.ClientID, in.ClientSecret = "", "" })
	q, back := f.signIn(t, "alice")
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); err != nil {
		t.Fatal(err)
	}
	conn, err := f.reg.Get(ctx, f.conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conn.OAuth.Registration != connectors.RegistrationDynamic || conn.OAuth.ClientID != q.Get("client_id") || !conn.OAuth.SecretStored {
		t.Fatalf("connector OAuth = %+v, authorize client_id %q", conn.OAuth, q.Get("client_id"))
	}
	_, back = f.signIn(t, "bob")
	if _, err := f.svc.Complete(ctx, "bob", completion(back)); err != nil {
		t.Fatal(err)
	}
	if n := f.as.Registrations(); n != 1 {
		t.Fatalf("%d registrations, want 1", n)
	}
}

func TestNoClientAndNoRegistrationNeedsAClient(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{NoRegistration: true}, func(in *registry.Input) { in.ClientID, in.ClientSecret = "", "" })
	if _, err := f.svc.Start(ctx, alice, f.conn, "alice", "alice"); !errors.Is(err, connectors.ErrNeedsClient) {
		t.Fatalf("Start = %v, want ErrNeedsClient", err)
	}
}

// Static endpoints on the row win over discovery, and send no resource.
func TestStaticEndpoints(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	in := registry.Input{
		Name: "Fake", URL: f.mcp.URL, AuthMode: connectors.AuthUser, Enabled: true,
		ClientID: connectorstest.ClientID, ClientSecret: connectorstest.ClientSecret, AllowedGroups: []string{"*"},
		AuthorizeURL: f.as.URL + "/authorize", TokenURL: f.as.URL + "/token", RevokeURL: f.as.URL + "/revoke", Rev: f.conn.Rev,
	}
	var err error
	if f.conn, err = f.reg.Update(ctx, alice, f.conn.ID, in); err != nil {
		t.Fatal(err)
	}
	f.mcp.HideMetadataHint(true)
	q, back := f.signIn(t, "alice")
	if q.Has("resource") {
		t.Fatalf("static endpoints sent resource %q", q.Get("resource"))
	}
	g, err := f.svc.Complete(ctx, "alice", completion(back))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Revoke(ctx, f.conn, g.Tokens.Refresh, "refresh_token"); err != nil {
		t.Fatal(err)
	}
	if r := f.as.Revocations(); len(r) != 1 || r[0].Token != g.Tokens.Refresh || r[0].Hint != "refresh_token" {
		t.Fatalf("revocations = %+v", r)
	}
}

func TestStaticEndpointsWithoutAClientNeedAClient(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	conn, err := f.reg.Update(ctx, alice, f.conn.ID, registry.Input{
		Name: "Fake", URL: f.mcp.URL, AuthMode: connectors.AuthUser, Enabled: true, AllowedGroups: []string{"*"},
		AuthorizeURL: f.as.URL + "/authorize", TokenURL: f.as.URL + "/token", Rev: f.conn.Rev,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Start(ctx, alice, conn, "alice", "alice"); !errors.Is(err, connectors.ErrNeedsClient) {
		t.Fatalf("Start = %v, want ErrNeedsClient", err)
	}
}

// Only the person who started a sign-in finishes it; a refused attempt
// spends the state.
func TestWrongPersonIsRefusedAndTheStateIsSpent(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	if _, err := f.svc.Complete(ctx, "bob", completion(back)); !errors.Is(err, connectors.ErrSignInExpired) {
		t.Fatalf("bob completes alice's sign-in: %v, want ErrSignInExpired", err)
	}
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); !errors.Is(err, connectors.ErrSignInExpired) {
		t.Fatalf("alice after bob tried: %v, want ErrSignInExpired", err)
	}
	if f.as.Exchanges() != 0 {
		t.Fatal("a refused sign-in reached the token endpoint")
	}
}

func TestStateIsSingleUse(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); !errors.Is(err, connectors.ErrSignInExpired) {
		t.Fatalf("second Complete = %v, want ErrSignInExpired", err)
	}
	if _, err := f.svc.Complete(ctx, "alice", oauth.Completion{State: "made-up", Code: "x"}); !errors.Is(err, connectors.ErrSignInExpired) {
		t.Fatalf("unknown state = %v, want ErrSignInExpired", err)
	}
}

func TestExpiredStateFails(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	f.now = f.now.Add(oauth.StateTTL + time.Second)
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); !errors.Is(err, connectors.ErrSignInExpired) {
		t.Fatalf("Complete after the state expired = %v, want ErrSignInExpired", err)
	}
}

// A connector changed since the sign-in started is refused.
func TestConnectorChangedSinceStartFails(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	if _, err := f.reg.SetTools(ctx, alice, f.conn.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Complete(ctx, "alice", completion(back)); !errors.Is(err, connectors.ErrSignInExpired) {
		t.Fatalf("Complete after a change = %v, want ErrSignInExpired", err)
	}
}

// An answer naming another issuer is refused when the server said it names
// itself (RFC 9207).
func TestIssuerMismatchFails(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	c := completion(back)
	c.Iss = "https://as.example.org"
	var oe *connectors.OAuthError
	if _, err := f.svc.Complete(ctx, "alice", c); !errors.As(err, &oe) || oe.Code != connectors.CodeIssuer {
		t.Fatalf("Complete with another iss = %v", err)
	}
	if f.as.Exchanges() != 0 {
		t.Fatal("the code was exchanged")
	}
}

func TestServerWithoutS256IsRefused(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{PlainOnly: true}, nil)
	var oe *connectors.OAuthError
	if _, err := f.svc.Start(ctx, alice, f.conn, "alice", "alice"); !errors.As(err, &oe) || oe.Code != connectors.CodeNoPKCE {
		t.Fatalf("Start = %v, want %s", err, connectors.CodeNoPKCE)
	}
}

// A bad code comes back as the vendor's error code only.
func TestBadCodeIsAnOAuthError(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	c := completion(back)
	c.Code = "code-wrong"
	var oe *connectors.OAuthError
	if _, err := f.svc.Complete(ctx, "alice", c); !errors.As(err, &oe) || oe.Code != "invalid_grant" {
		t.Fatalf("Complete with a bad code = %v", err)
	}
}

func TestRefreshRotatesAndInvalidGrant(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{}, nil)
	_, back := f.signIn(t, "alice")
	g, err := f.svc.Complete(ctx, "alice", completion(back))
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.svc.Refresh(ctx, f.conn, g.Tokens.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	if next.Access == g.Tokens.Access || next.Refresh == "" || next.Refresh == g.Tokens.Refresh || !f.as.Valid(next.Access, f.mcp.URL) {
		t.Fatal("refresh did not rotate the tokens")
	}
	f.as.RefuseRefresh(true)
	if _, err := f.svc.Refresh(ctx, f.conn, next.Refresh); !connectors.IsInvalidGrant(err) {
		t.Fatalf("Refresh while refused = %v, want invalid_grant", err)
	}
}

func TestRevokeWithoutAnEndpoint(t *testing.T) {
	t.Parallel()
	f := setup(t, connectorstest.AuthServerOptions{NoRevocation: true}, nil)
	if err := f.svc.Revoke(ctx, f.conn, "at-1", "access_token"); !errors.Is(err, vault.ErrNoRevocation) {
		t.Fatalf("Revoke = %v, want ErrNoRevocation", err)
	}
}

// base64Len is the decoded length of a base64url value without padding.
func base64Len(s string) (int, error) {
	if strings.ContainsAny(s, "+/=") {
		return 0, errors.New("not base64url")
	}
	return len(s) * 6 / 8, nil
}
