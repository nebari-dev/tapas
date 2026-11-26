package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/mcp"
)

// fakeBroker offers one tool and answers every connector call itself,
// recording who called.
type fakeBroker struct {
	mu      sync.Mutex
	callers []connectors.Caller
	names   []string
}

func (b *fakeBroker) Tools(_ context.Context, c connectors.Caller) []*sdk.Tool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.callers = append(b.callers, c)
	return []*sdk.Tool{{Name: "fake__echo", Description: "Fake: echo", InputSchema: map[string]any{"type": "object"}}}
}

func (b *fakeBroker) Call(_ context.Context, c connectors.Caller, name string, args json.RawMessage) (*sdk.CallToolResult, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.names = append(b.names, name)
	b.callers = append(b.callers, c)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "from " + name + " " + string(args)}}}, true
}

type bearer struct{ tok string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func serve(t *testing.T, b mcp.Broker) *sdk.ClientSession {
	t.Helper()
	verify := func(_ context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
		info := &auth.TokenInfo{UserID: "alice", Scopes: []string{mcp.ScopeRead}}
		mcp.SetCaller(info, connectors.Caller{
			Principal: authz.Principal{ID: "alice"}, Token: token.Token{ID: "t-1", Owner: "alice"},
		})
		return info, nil
	}
	h := mcp.NewHandler(mcp.Config{
		API: http.NotFoundHandler(), Doc: &huma.OpenAPI{}, Verify: verify, Broker: b,
		Actions: []actions.Info{{ID: actions.Stop, Summary: "Stop an app"}},
	})
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: s.URL, HTTPClient: &http.Client{Transport: bearer{"tok"}}, MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// The broker's tools are listed next to Tapas's own, for the caller the
// bearer check found, and a connector tool's call goes to the broker.
func TestBrokerToolsAreListedAndCalled(t *testing.T) {
	t.Parallel()
	b := &fakeBroker{}
	cs := serve(t, b)
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range list.Tools {
		names[tl.Name] = true
	}
	if !names["fake__echo"] || !names["stop_workspace"] {
		t.Fatalf("tools %v", names)
	}
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "fake__echo", Arguments: map[string]any{"a": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Content[0].(*sdk.TextContent).Text; got != `from fake__echo {"a":1}` {
		t.Fatalf("call answered %q", got)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.callers {
		if c.Principal.ID != "alice" || c.Token.ID != "t-1" || c.Client != "claude-code" {
			t.Fatalf("caller %+v", c)
		}
	}
}

// Tapas's own tools never get to the broker, and the broker is never
// asked without a caller.
func TestOwnToolsDoNotReachTheBroker(t *testing.T) {
	t.Parallel()
	b := &fakeBroker{}
	cs := serve(t, b)
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "stop_workspace", Arguments: map[string]any{"name": "sales"}}); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.names) != 0 {
		t.Fatalf("broker got %v", b.names)
	}
}
