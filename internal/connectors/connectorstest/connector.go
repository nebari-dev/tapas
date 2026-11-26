// Package connectorstest holds fakes for testing connectors without a
// network beyond httptest: an MCP connector that records every call with
// the bearer it was made with.
package connectorstest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
)

// Call is one tool call the connector answered: the tool, the bearer it
// came with and its arguments exactly as received.
type Call struct {
	Tool      string
	Bearer    string
	Arguments json.RawMessage
}

// Connector is a fake MCP server behind bearer auth. It has a read tool
// echo, which answers its arguments as text, and a write tool post, which
// answers "posted". Knobs make it answer 401, hang, or return oversized
// results.
type Connector struct {
	// URL is its MCP endpoint (plain http on a loopback address).
	URL string

	mu           sync.Mutex
	bearers      []string
	calls        []Call
	lists        int
	requests     int
	unauthorized bool
	hang         bool
	oversize     int
	release      chan struct{}
}

// NewConnector starts a connector that accepts the bearers given. It stops
// when the test ends.
func NewConnector(t *testing.T, bearers ...string) *Connector {
	t.Helper()
	c := &Connector{bearers: bearers, release: make(chan struct{})}
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{
		Name: "echo", Description: "Answers its arguments",
		InputSchema: map[string]any{"type": "object"}, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true},
	}, c.handle("echo"))
	server.AddTool(&sdk.Tool{
		Name: "post", Description: "Posts a message",
		InputSchema: map[string]any{"type": "object"},
	}, c.handle("post"))
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			if method == "tools/list" {
				c.mu.Lock()
				c.lists++
				c.mu.Unlock()
			}
			return next(ctx, method, req)
		}
	})
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	authed := auth.RequireBearerToken(c.verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.requests++
		refuse := c.unauthorized
		c.mu.Unlock()
		if refuse {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authed.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		close(c.release)
		s.Close()
	})
	c.URL = s.URL + "/mcp"
	return c
}

func (c *Connector) verify(_ context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !slices.Contains(c.bearers, raw) {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: raw}, nil
}

func (c *Connector) handle(name string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		bearer := ""
		if req.Extra != nil {
			bearer = strings.TrimPrefix(req.Extra.Header.Get("Authorization"), "Bearer ")
		}
		c.mu.Lock()
		c.calls = append(c.calls, Call{Tool: name, Bearer: bearer, Arguments: slices.Clone(req.Params.Arguments)})
		hang, oversize := c.hang, c.oversize
		c.mu.Unlock()
		if hang {
			select {
			case <-ctx.Done():
			case <-c.release:
			}
			return nil, ctx.Err()
		}
		text := "posted"
		switch {
		case oversize > 0:
			text = strings.Repeat("x", oversize)
		case name == "echo":
			text = string(req.Params.Arguments)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil
	}
}

// Calls are the tool calls answered so far, in order.
func (c *Connector) Calls() []Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// Lists is how many tools/list requests it answered.
func (c *Connector) Lists() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lists
}

// Requests is how many HTTP requests reached it.
func (c *Connector) Requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

// Unauthorized makes it answer every request with 401 while on.
func (c *Connector) Unauthorized(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unauthorized = on
}

// Hang makes its tools never answer while on, until the caller gives up.
func (c *Connector) Hang(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hang = on
}

// Oversize makes its tools answer n bytes of text; 0 answers normally.
func (c *Connector) Oversize(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.oversize = n
}

// StaticToken is a token source that gives every connector and person the
// same bearer; empty means nobody connected.
type StaticToken string

func (s StaticToken) Token(_ context.Context, _ connectors.Connector, _ string) (string, error) {
	if s == "" {
		return "", connectors.ErrNotConnected
	}
	return string(s), nil
}
