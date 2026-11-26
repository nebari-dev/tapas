//go:build !local

package gateway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/model"
)

// newConnectorsFixture is a gateway serving /mcp with connectors on, whose
// connector calls carry bearer and reach loopback servers (the guard has
// its own tests).
func newConnectorsFixture(t *testing.T, bearer string) *mcpFixture {
	t.Helper()
	f := apitest.New(t, apitest.Options{
		Mcp: true, Connectors: true, ConnectorInternalHosts: []string{"127.0.0.1"},
		ConnectorTokens: connectorstest.StaticToken(bearer), ConnectorHTTP: &http.Client{},
	})
	s := httptest.NewServer(f.Srv)
	t.Cleanup(s.Close)
	return &mcpFixture{Fixture: f, url: s.URL, alice: f.LoginAlice()}
}

func toolNames(t *testing.T, f *mcpFixture, tok string) map[string]bool {
	t.Helper()
	list, err := f.connect(t, tok).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tl := range list.Tools {
		out[tl.Name] = true
	}
	return out
}

// With a connector row pointing at the fake, an MCP client lists its tools
// next to Tapas's own and calls them through /mcp; disabling the connector
// takes its tools away.
func TestConnectorToolsThroughMCP(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok-fake")
	f := newConnectorsFixture(t, "tok-fake")
	in := registry.Input{
		Slug: "fake", Name: "Fake", URL: fake.URL, AuthMode: connectors.AuthShared, Enabled: true,
		AllowedGroups: []string{"*"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}
	conn, err := f.Connectors.Create(context.Background(), model.Actor{Person: "alice"}, in)
	if err != nil {
		t.Fatal(err)
	}
	tok := f.NewToken(f.alice, `{"name":"agent","scope":"read"}`).Token
	names := toolNames(t, f, tok)
	if !names["fake__echo"] || !names["fake__post"] || !names["list_workspaces"] {
		t.Fatalf("tools %v", names)
	}
	res, text := callTool(t, f.connect(t, tok), "fake__echo", map[string]any{"text": "hello"})
	if res.IsError || text != `{"text":"hello"}` {
		t.Fatalf("echo: %v %q", res.IsError, text)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Bearer != "tok-fake" || calls[0].Tool != "echo" {
		t.Fatalf("calls %+v", calls)
	}
	in.Enabled, in.Rev = false, conn.Rev
	if _, err := f.Connectors.Update(context.Background(), model.Actor{Person: "alice"}, conn.ID, in); err != nil {
		t.Fatal(err)
	}
	if names := toolNames(t, f, tok); names["fake__echo"] || names["fake__post"] {
		t.Fatalf("disabled connector still lists %v", names)
	}
}

// Tapas's own tool names never hold the separator, so a name that does is
// always a connector's.
func TestOwnToolNamesNeverHoldTheSeparator(t *testing.T) {
	t.Parallel()
	f := newMCPFixture(t)
	for name := range toolNames(t, f, f.NewToken(f.alice, `{"name":"agent","scope":"read-write"}`).Token) {
		if strings.Contains(name, connectors.ToolSeparator) {
			t.Errorf("tool %s holds %q", name, connectors.ToolSeparator)
		}
	}
}

// A coding agent's app token reaches connectors: the bearer check tells
// the broker the token's app is a coding agent. (Other app tokens are
// refused by the broker, tested there.)
func TestOnlyAgentAppTokensReachConnectors(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok-fake")
	agent := newAgentFixtureWith(t, true, apitest.Options{
		Mcp: true, Connectors: true, ConnectorInternalHosts: []string{"127.0.0.1"},
		ConnectorTokens: connectorstest.StaticToken("tok-fake"), ConnectorHTTP: &http.Client{},
	})
	s := httptest.NewServer(agent.Srv)
	t.Cleanup(s.Close)
	f := &mcpFixture{Fixture: agent.Fixture, url: s.URL, alice: agent.alice}
	if _, err := f.Connectors.Create(context.Background(), model.Actor{Person: "alice"}, registry.Input{
		Slug: "fake", Name: "Fake", URL: fake.URL, AuthMode: connectors.AuthShared, Enabled: true, AllowedGroups: []string{"*"},
		ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}); err != nil {
		t.Fatal(err)
	}
	if names := toolNames(t, f, agent.token); !names["fake__echo"] {
		t.Fatalf("agent token sees %v", names)
	}
	res, text := callTool(t, f.connect(t, agent.token), "fake__echo", map[string]any{})
	if res.IsError || text != "{}" {
		t.Fatalf("agent token call: %v %q", res.IsError, text)
	}
}

// signIn connects through the API with cookie c at path (a connect route)
// and the fake vendor; person is who finishes it.
func signIn(t *testing.T, f *mcpFixture, as *connectorstest.AuthServer, path string, c *http.Cookie) string {
	t.Helper()
	w := f.Do("POST", path, "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s: %d %s", path, w.Code, w.Body)
	}
	back, err := as.Authorize(apitest.Decode[struct{ AuthorizeURL string }](t, w).AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"state":%q,"code":%q,"iss":%q}`, back.Get("state"), back.Get("code"), back.Get("iss"))
}

func complete(t *testing.T, f *mcpFixture, body string, c *http.Cookie, want int) {
	t.Helper()
	if w := f.Do("POST", "/api/connectors/oauth/complete", body, c); w.Code != want {
		t.Fatalf("complete: %d %s, want %d", w.Code, w.Body, want)
	}
}

// End to end: alice connects her own account through the fake vendor, and
// her agent's /mcp call reaches the connector with her token, refreshed
// when the connector stops taking it; bob cannot finish her sign-in and has
// no tools of hers. A shared connector's calls use the account an admin
// connected, for bob too.
func TestConnectedAccountsThroughMCP(t *testing.T) {
	t.Parallel()
	af := apitest.New(t, apitest.Options{
		Mcp: true, Connectors: true, PublicURL: "https://tapas.example.org", ConnectorInternalHosts: []string{"127.0.0.1"},
		ConnectorHTTP: httpx.Guard{}.Client(), People: []apitest.TestPerson{apitest.SignedIn("bob", time.Time{})},
	})
	s := httptest.NewServer(af.Srv)
	t.Cleanup(s.Close)
	f := &mcpFixture{Fixture: af, url: s.URL, alice: af.LoginAlice()}
	bob := f.Session(apitest.PersonID("bob"))
	ctx := context.Background()
	as := connectorstest.NewAuthServer(t)
	mine := connectorstest.NewOAuthConnector(t, as)
	conn, err := f.Connectors.Create(ctx, model.Actor{Person: "admin"}, registry.Input{
		Slug: "mine", Name: "Mine", URL: mine.URL, AuthMode: connectors.AuthUser, Enabled: true, AllowedGroups: []string{"*"},
		ClientID: connectorstest.ClientID, ClientSecret: connectorstest.ClientSecret,
		ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := signIn(t, f, as, "/api/connectors/"+conn.ID+"/connect", f.alice)
	complete(t, f, body, bob, http.StatusBadRequest)
	complete(t, f, body, f.alice, http.StatusBadRequest)
	complete(t, f, signIn(t, f, as, "/api/connectors/"+conn.ID+"/connect", f.alice), f.alice, http.StatusOK)

	aliceTok := f.NewToken(f.alice, `{"name":"agent","scope":"read"}`).Token
	bobTok := f.NewToken(bob, `{"name":"agent","scope":"read"}`).Token
	if names := toolNames(t, f, aliceTok); !names["mine__echo"] {
		t.Fatalf("alice's tools %v", names)
	}
	if names := toolNames(t, f, bobTok); names["mine__echo"] {
		t.Fatalf("bob sees alice's connector's tools: %v", names)
	}
	res, text := callTool(t, f.connect(t, aliceTok), "mine__echo", map[string]any{"n": 1})
	if res.IsError || text != `{"n":1}` {
		t.Fatalf("alice's echo: %v %q", res.IsError, text)
	}
	stored, err := f.Vault.Token(ctx, conn, apitest.LocalAlice())
	if err != nil {
		t.Fatal(err)
	}
	if calls := mine.Calls(); len(calls) != 1 || calls[0].Bearer != stored || !as.Valid(stored, mine.URL) {
		t.Fatalf("calls %+v; alice's stored token %q", calls, stored)
	}
	if res, _ := callTool(t, f.connect(t, bobTok), "mine__echo", map[string]any{}); !res.IsError || len(mine.Calls()) != 1 {
		t.Fatal("bob's call reached alice's connector")
	}

	// The vendor stops taking alice's token: the call refreshes it and
	// goes through once more.
	as.Expire()
	if res, text := callTool(t, f.connect(t, aliceTok), "mine__echo", map[string]any{"n": 2}); res.IsError || text != `{"n":2}` {
		t.Fatalf("echo after the token expired: %v %q", res.IsError, text)
	}
	calls := mine.Calls()
	if len(calls) != 2 || calls[1].Bearer == stored || as.Refreshes() != 1 {
		t.Fatalf("calls %+v after %d refreshes", calls, as.Refreshes())
	}

	sharedSrv := connectorstest.NewOAuthConnector(t, as)
	shared, err := f.Connectors.Create(ctx, model.Actor{Person: "admin"}, registry.Input{
		Slug: "team", Name: "Team", URL: sharedSrv.URL, AuthMode: connectors.AuthShared, Enabled: true, AllowedGroups: []string{"*"},
		ClientID: connectorstest.ClientID, ClientSecret: connectorstest.ClientSecret,
		ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := f.Session(apitest.AdminID, "javo-admins")
	complete(t, f, signIn(t, f, as, "/api/admin/connectors/"+shared.ID+"/connect", admin), admin, http.StatusOK)
	if res, text := callTool(t, f.connect(t, bobTok), "team__echo", map[string]any{"who": "bob"}); res.IsError || text != `{"who":"bob"}` {
		t.Fatalf("bob's shared echo: %v %q", res.IsError, text)
	}
	adminTok, err := f.Vault.Token(ctx, shared, "")
	if err != nil {
		t.Fatal(err)
	}
	if calls := sharedSrv.Calls(); len(calls) != 1 || calls[0].Bearer != adminTok {
		t.Fatalf("shared calls %+v", calls)
	}
}
