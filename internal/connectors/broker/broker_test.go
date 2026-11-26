package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/broker"
	"github.com/nebari-dev/javo/internal/connectors/client"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/policy"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	ctx   = context.Background()
	admin = model.Actor{Person: "admin"}
)

const bearer = "tok-shared"

type fixture struct {
	reg  *registry.Registry
	fake *connectorstest.Connector
	b    *broker.Broker
	conn connectors.Connector
}

type options struct {
	tokens connectors.TokenSource
	policy policy.Checker
}

func newFixture(t *testing.T, o options) *fixture {
	t.Helper()
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(registry.Config{DB: storetest.New(t), Seal: key, InternalHosts: []string{"127.0.0.1"}})
	fake := connectorstest.NewConnector(t, bearer)
	conn, err := reg.Create(ctx, admin, registry.Input{
		Slug: "fake", Name: "Fake", URL: fake.URL, AuthMode: connectors.AuthShared, Enabled: true,
		AllowedGroups: []string{"sales"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.tokens == nil {
		o.tokens = connectorstest.StaticToken(bearer)
	}
	b := broker.New(broker.Config{
		Connectors: reg, Forwarder: &client.Client{HTTP: &http.Client{}}, Tokens: o.tokens, Policy: o.policy,
		PublicURL: "https://tapas.example.org",
	})
	return &fixture{reg: reg, fake: fake, b: b, conn: conn}
}

func alice() connectors.Caller {
	return connectors.Caller{
		Principal: authz.Principal{ID: "alice", Groups: []string{"sales"}}, Roles: []authz.Role{authz.RoleMember},
		Token: token.Token{ID: "t-1", Owner: "alice"}, Client: "claude-code",
	}
}

func names(tools []*sdk.Tool) map[string]*sdk.Tool {
	out := map[string]*sdk.Tool{}
	for _, t := range tools {
		out[t.Name] = t
	}
	return out
}

func text(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) != 1 {
		t.Fatalf("result %+v", res)
	}
	return res.Content[0].(*sdk.TextContent).Text
}

func call(t *testing.T, f *fixture, c connectors.Caller, name, args string) *sdk.CallToolResult {
	t.Helper()
	res, ok := f.b.Call(ctx, c, name, json.RawMessage(args))
	if !ok {
		t.Fatalf("%s was not taken as a connector tool", name)
	}
	return res
}

func TestToolsAreNamespacedAndMarkedByTheAdminsModes(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	tools := names(f.b.Tools(ctx, alice()))
	echo, post := tools["fake__echo"], tools["fake__post"]
	if len(tools) != 2 || echo == nil || post == nil {
		t.Fatalf("tools %v", tools)
	}
	if !strings.HasPrefix(echo.Description, "Fake: ") || echo.Annotations == nil || !echo.Annotations.ReadOnlyHint {
		t.Errorf("echo %+v %+v", echo, echo.Annotations)
	}
	// post has no mode, so it is a write, whatever the connector claims.
	if post.Annotations == nil || post.Annotations.ReadOnlyHint || !strings.Contains(post.Description, "Proposes the change only") {
		t.Errorf("post %+v %+v", post, post.Annotations)
	}
	in := registry.Input{
		Slug: "fake", Name: "Fake", URL: f.fake.URL, AuthMode: connectors.AuthShared, Enabled: true, Rev: f.conn.Rev,
		AllowedGroups: []string{"sales"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead, "post": connectors.ToolOff},
	}
	if _, err := f.reg.Update(ctx, admin, f.conn.ID, in); err != nil {
		t.Fatal(err)
	}
	if tools := names(f.b.Tools(ctx, alice())); len(tools) != 1 || tools["fake__echo"] == nil {
		t.Fatalf("with post off: %v", tools)
	}
	res := call(t, f, alice(), "fake__post", `{}`)
	if !res.IsError || text(t, res) != reasons.Msg(reasons.MsgConnectorToolNotFound) || len(f.fake.Calls()) != 0 {
		t.Fatalf("calling an off tool: %+v", res)
	}
}

// The upstream list is cached per connector revision and subject: a second
// tools/list does not reach the connector, a saved change does.
func TestTheUpstreamListIsCachedPerRevision(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	f.b.Tools(ctx, alice())
	f.b.Tools(ctx, alice())
	call(t, f, alice(), "fake__echo", `{}`)
	if n := f.fake.Lists(); n != 1 {
		t.Fatalf("listed %d times", n)
	}
	in := registry.Input{
		Slug: "fake", Name: "Renamed", URL: f.fake.URL, AuthMode: connectors.AuthShared, Enabled: true, Rev: f.conn.Rev,
		AllowedGroups: []string{"sales"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}
	if _, err := f.reg.Update(ctx, admin, f.conn.ID, in); err != nil {
		t.Fatal(err)
	}
	f.b.Tools(ctx, alice())
	if n := f.fake.Lists(); n != 2 {
		t.Fatalf("after a change listed %d times", n)
	}
}

func TestAReadIsForwardedWithTheConnectionsToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	res := call(t, f, alice(), "fake__echo", `{"text":"hello"}`)
	if res.IsError || text(t, res) != `{"text":"hello"}` {
		t.Fatalf("echo %+v", res)
	}
	calls := f.fake.Calls()
	if len(calls) != 1 || calls[0].Tool != "echo" || calls[0].Bearer != bearer {
		t.Fatalf("calls %+v", calls)
	}
}

// Writes become plans in a later change; until then they are refused and
// never reach the connector.
func TestAWriteIsNotForwarded(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	res := call(t, f, alice(), "fake__post", `{"text":"hello"}`)
	if !res.IsError || len(f.fake.Calls()) != 0 {
		t.Fatalf("post %+v, calls %v", res, f.fake.Calls())
	}
}

func TestOnlyConnectorNamesAreTaken(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	if res, ok := f.b.Call(ctx, alice(), "list_workspaces", nil); ok || res != nil {
		t.Fatalf("took a Tapas tool: %v", res)
	}
}

// A disabled connector, one the caller's groups may not use, an unknown
// connector and an unknown tool all answer the same, so a caller cannot
// tell which connectors exist.
func TestUnusableConnectorsLookLikeUnknownTools(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	bob := alice()
	bob.Principal = authz.Principal{ID: "bob", Groups: []string{"support"}}
	if tools := f.b.Tools(ctx, bob); len(tools) != 0 {
		t.Fatalf("bob sees %v", names(tools))
	}
	notFound := reasons.Msg(reasons.MsgConnectorToolNotFound)
	for _, c := range []struct {
		caller connectors.Caller
		name   string
	}{{bob, "fake__echo"}, {alice(), "nope__echo"}, {alice(), "fake__nope"}, {alice(), "Fake__echo"}} {
		if res := call(t, f, c.caller, c.name, `{}`); !res.IsError || text(t, res) != notFound {
			t.Errorf("%s as %s: %+v", c.name, c.caller.Principal.ID, res)
		}
	}
	in := registry.Input{
		Slug: "fake", Name: "Fake", URL: f.fake.URL, AuthMode: connectors.AuthShared, Enabled: false, Rev: f.conn.Rev,
		AllowedGroups: []string{"sales"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}
	if _, err := f.reg.Update(ctx, admin, f.conn.ID, in); err != nil {
		t.Fatal(err)
	}
	if tools := f.b.Tools(ctx, alice()); len(tools) != 0 {
		t.Fatalf("disabled connector lists %v", names(tools))
	}
	if res := call(t, f, alice(), "fake__echo", `{}`); text(t, res) != notFound {
		t.Fatalf("disabled connector: %+v", res)
	}
	if len(f.fake.Calls()) != 0 {
		t.Fatalf("calls %v", f.fake.Calls())
	}
}

// A plain app's token sees no connector tools and is told why on a call;
// a coding agent's app token may use them.
func TestOnlyPersonalAndAgentTokens(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	app := alice()
	app.Token.App = &token.App{Namespace: "ns", Name: "helper-1", Short: "helper"}
	if tools := f.b.Tools(ctx, app); len(tools) != 0 {
		t.Fatalf("app token sees %v", names(tools))
	}
	if res := call(t, f, app, "fake__echo", `{}`); text(t, res) != reasons.Msg(reasons.MsgConnectorNotForAppTokens) {
		t.Fatalf("app token: %+v", res)
	}
	app.AgentApp, app.Agent = true, "helper"
	if res := call(t, f, app, "fake__echo", `{}`); res.IsError {
		t.Fatalf("agent token: %+v", res)
	}
}

func TestNotConnectedShowsNoToolsAndSaysWhereToConnect(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{tokens: connectorstest.StaticToken("")})
	if tools := f.b.Tools(ctx, alice()); len(tools) != 0 {
		t.Fatalf("tools %v", names(tools))
	}
	res := call(t, f, alice(), "fake__echo", `{}`)
	if want := reasons.Msg(reasons.MsgConnectorNotConnected, "Fake", "https://tapas.example.org/settings/connections"); text(t, res) != want {
		t.Fatalf("got %q", text(t, res))
	}
}

type denyAll struct{ seen *policy.Input }

func (d denyAll) Check(_ context.Context, in policy.Input) (policy.Decision, error) {
	*d.seen = in
	return policy.Decision{Effect: policy.Deny, Reason: "Reads of fake are off today."}, nil
}

type failing struct{}

func (failing) Check(context.Context, policy.Input) (policy.Decision, error) {
	return policy.Decision{}, errors.New("policy server down")
}

func TestPolicyIsAskedBeforeARead(t *testing.T) {
	t.Parallel()
	var seen policy.Input
	f := newFixture(t, options{policy: denyAll{&seen}})
	res := call(t, f, alice(), "fake__echo", `{"text":"hello"}`)
	if !res.IsError || !strings.Contains(text(t, res), "Reads of fake are off today") || len(f.fake.Calls()) != 0 {
		t.Fatalf("denied read %+v", res)
	}
	if seen.Phase != policy.PhaseRead || seen.Person != "alice" || seen.Connector != "fake" || seen.Tool != "echo" ||
		seen.TokenID != "t-1" || string(seen.Arguments) != `{"text":"hello"}` || seen.Write {
		t.Fatalf("policy saw %+v", seen)
	}
	f = newFixture(t, options{policy: failing{}})
	if res := call(t, f, alice(), "fake__echo", `{}`); !res.IsError || len(f.fake.Calls()) != 0 {
		t.Fatalf("failing policy %+v", res)
	}
}

func TestArgumentsMustBeASmallJSONObject(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	big := `{"text":"` + strings.Repeat("x", broker.MaxArguments) + `"}`
	for _, args := range []string{`[1]`, `"x"`, `null`, `{`, big} {
		if res := call(t, f, alice(), "fake__echo", args); !res.IsError {
			t.Errorf("%.20s: %+v", args, res)
		}
	}
	if res := call(t, f, alice(), "fake__echo", ``); res.IsError {
		t.Fatalf("no arguments: %+v", res)
	}
	if len(f.fake.Calls()) != 1 {
		t.Fatalf("calls %d", len(f.fake.Calls()))
	}
}

// A connector that fails to list is left out; the others still are listed.
func TestAFailingConnectorIsLeftOutOfTheList(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	broken := connectorstest.NewConnector(t, "other-token")
	if _, err := f.reg.Create(ctx, admin, registry.Input{
		Slug: "broken", Name: "Broken", URL: broken.URL, AuthMode: connectors.AuthShared, Enabled: true, AllowedGroups: []string{"*"},
	}); err != nil {
		t.Fatal(err)
	}
	if tools := names(f.b.Tools(ctx, alice())); len(tools) != 2 || tools["fake__echo"] == nil {
		t.Fatalf("tools %v", tools)
	}
}

func TestATimeoutIsAToolError(t *testing.T) {
	t.Parallel()
	key, _ := secrets.NewKey()
	reg := registry.New(registry.Config{DB: storetest.New(t), Seal: key, InternalHosts: []string{"127.0.0.1"}})
	fake := connectorstest.NewConnector(t, bearer)
	if _, err := reg.Create(ctx, admin, registry.Input{
		Slug: "fake", Name: "Fake", URL: fake.URL, AuthMode: connectors.AuthUser, Enabled: true, AllowedGroups: []string{"sales"},
		ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}); err != nil {
		t.Fatal(err)
	}
	b := broker.New(broker.Config{
		Connectors: reg, Forwarder: &client.Client{HTTP: &http.Client{}, CallTimeout: 100e6}, Tokens: connectorstest.StaticToken(bearer),
	})
	fake.Hang(true)
	res, _ := b.Call(ctx, alice(), "fake__echo", json.RawMessage(`{}`))
	if !res.IsError || !strings.Contains(text(t, res), "in time") {
		t.Fatalf("hang %+v", res)
	}
}
