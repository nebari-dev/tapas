package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors/client"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
)

var ctx = context.Background()

// Tests reach the loopback fake with a plain client; the guard has its own
// tests in internal/httpx.
func newClient() *client.Client { return &client.Client{HTTP: &http.Client{}} }

func TestListsAndCallsWithTheBearerGivenPerCall(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok-alice", "tok-bob")
	c := newClient()
	target := client.Target{URL: fake.URL}
	tools, err := c.ListTools(ctx, target, "tok-alice")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	if len(tools) != 2 || !names["echo"] || !names["post"] {
		t.Fatalf("tools %v", names)
	}
	args := json.RawMessage(`{"text":"hello"}`)
	res, err := c.CallTool(ctx, target, "tok-bob", "echo", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("result %+v", res)
	}
	if _, err := c.CallTool(ctx, target, "tok-alice", "post", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls()
	if len(calls) != 2 || calls[0].Bearer != "tok-bob" || calls[1].Bearer != "tok-alice" || string(calls[0].Arguments) != string(args) {
		t.Fatalf("calls %+v", calls)
	}
}

func TestAWrongBearerIsUnauthorized(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok-alice")
	c := newClient()
	if _, err := c.ListTools(ctx, client.Target{URL: fake.URL}, "tok-mallory"); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("wrong bearer: %v", err)
	}
	fake.Unauthorized(true)
	if _, err := c.CallTool(ctx, client.Target{URL: fake.URL}, "tok-alice", "echo", nil); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("401: %v", err)
	}
}

func TestAHangingCallHitsTheDeadline(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok")
	fake.Hang(true)
	c := newClient()
	c.CallTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err := c.CallTool(ctx, client.Target{URL: fake.URL}, "tok", "echo", json.RawMessage(`{}`))
	if !errors.Is(err, client.ErrTimeout) || time.Since(start) > 5*time.Second {
		t.Fatalf("hang: %v after %s", err, time.Since(start))
	}
}

// A result past the tool cap, and an answer past the HTTP body cap, are
// refused whole: no part of them comes back.
func TestAnOversizedResultIsCut(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok")
	c := newClient()
	for _, n := range []int{client.MaxResult + 1, client.MaxBody + 1} {
		fake.Oversize(n)
		res, err := c.CallTool(ctx, client.Target{URL: fake.URL}, "tok", "echo", json.RawMessage(`{}`))
		if !errors.Is(err, client.ErrTooLarge) || res != nil {
			t.Fatalf("%d bytes: %v %v", n, res, err)
		}
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	t.Parallel()
	fake := connectorstest.NewConnector(t, "tok")
	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fake.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(moved.Close)
	if _, err := newClient().ListTools(ctx, client.Target{URL: moved.URL + "/mcp"}, "tok"); err == nil {
		t.Fatal("followed a redirect")
	}
	if fake.Requests() != 0 {
		t.Fatalf("the redirect target was reached %d times", fake.Requests())
	}
}

// serveTools starts an MCP server (no auth) listing tools, pageSize per
// page.
func serveTools(t *testing.T, pageSize int, tools ...*sdk.Tool) string {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fake", Version: "1"}, &sdk.ServerOptions{PageSize: pageSize})
	for _, tl := range tools {
		server.AddTool(tl, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		})
	}
	s := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(s.Close)
	return s.URL
}

func tool(name string) *sdk.Tool {
	return &sdk.Tool{Name: name, Description: "a tool", InputSchema: map[string]any{"type": "object"}}
}

// A tool whose description, title or whole definition is past its cap is
// left out; the others are listed.
func TestOversizedToolsAreLeftOut(t *testing.T) {
	t.Parallel()
	long := func(n int) string { return strings.Repeat("x", n) }
	verbose, titled, annotated, huge := tool("verbose"), tool("titled"), tool("annotated"), tool("huge")
	verbose.Description = long(client.MaxToolDescription + 1)
	titled.Title = long(client.MaxToolTitle + 1)
	annotated.Annotations = &sdk.ToolAnnotations{Title: long(client.MaxToolTitle + 1)}
	huge.InputSchema = map[string]any{"type": "object", "description": long(client.MaxTool)}
	url := serveTools(t, 2, tool("small"), verbose, titled, annotated, huge, tool("other"))
	tools, err := newClient().ListTools(ctx, client.Target{URL: url}, "tok")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"other", "small"}) {
		t.Fatalf("tools %v", names)
	}
}

// A list whose tools together pass MaxToolList bytes is refused, however
// small each page is.
func TestAToolListPastTheTotalCapIsRefused(t *testing.T) {
	t.Parallel()
	var tools []*sdk.Tool
	for i := range client.MaxToolList/(client.MaxToolDescription) + 1 {
		tl := tool(fmt.Sprintf("t%d", i))
		tl.Description = strings.Repeat("x", client.MaxToolDescription)
		tools = append(tools, tl)
	}
	url := serveTools(t, 5, tools...)
	if got, err := newClient().ListTools(ctx, client.Target{URL: url}, "tok"); !errors.Is(err, client.ErrTooLarge) || got != nil {
		t.Fatalf("%d tools, %v", len(got), err)
	}
}
