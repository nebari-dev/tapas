//go:build !local

package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
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
