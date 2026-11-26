package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
