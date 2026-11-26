// Package client speaks MCP (Streamable HTTP) to a connector's server: it
// lists its tools and calls one. Each call opens its own session
// (initialize, the request, close) and carries the bearer it is given, so
// nothing is shared between people, calls or gateway replicas.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/model"
)

// Target is where a connector's MCP server is.
type Target struct{ URL string }

// Forwarder lists and calls a connector's tools with the bearer given.
type Forwarder interface {
	ListTools(ctx context.Context, t Target, bearer string) ([]*sdk.Tool, error)
	CallTool(ctx context.Context, t Target, bearer, name string, args json.RawMessage) (*sdk.CallToolResult, error)
}

// Limits of what a connector is waited for and given.
const (
	ConnectTimeout = 10 * time.Second
	ListTimeout    = 15 * time.Second
	CallTimeout    = 60 * time.Second
	// MaxBody is the most bytes of one HTTP answer read; MaxResult the
	// largest tool result (as JSON) passed on; MaxTools the most tools a
	// connector may list.
	MaxBody   = 4 << 20
	MaxResult = 1 << 20
	MaxTools  = 1000
)

// Errors a call fails with, by class. Their text holds nothing the
// connector sent.
var (
	ErrUnauthorized = errors.New("the connector refused the token")
	ErrTimeout      = errors.New("the connector did not answer in time")
	ErrTooLarge     = errors.New("the connector's answer is too large")
	ErrUpstream     = errors.New("the connector could not be reached or failed")
)

// Client is the Forwarder over HTTP.
type Client struct {
	// HTTP sends the requests: the guarded client (httpx.Guard) in
	// production. Its transport is used; redirects are never followed.
	HTTP *http.Client
	// Timeouts; zero is the default above.
	ConnectTimeout, ListTimeout, CallTimeout time.Duration
}

var _ Forwarder = (*Client)(nil)

// ListTools lists every tool of the connector.
func (c *Client) ListTools(ctx context.Context, t Target, bearer string) ([]*sdk.Tool, error) {
	var tools []*sdk.Tool
	err := c.session(ctx, t, bearer, or(c.ListTimeout, ListTimeout), func(ctx context.Context, cs *sdk.ClientSession) error {
		for tl, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			if len(tools) == MaxTools {
				return ErrTooLarge
			}
			tools = append(tools, tl)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tools, nil
}

// CallTool calls the tool name with args as they are and returns its
// result, whole or not at all.
func (c *Client) CallTool(ctx context.Context, t Target, bearer, name string, args json.RawMessage) (*sdk.CallToolResult, error) {
	var res *sdk.CallToolResult
	err := c.session(ctx, t, bearer, or(c.CallTimeout, CallTimeout), func(ctx context.Context, cs *sdk.ClientSession) error {
		p := &sdk.CallToolParams{Name: name}
		if len(args) > 0 {
			p.Arguments = args
		}
		r, err := cs.CallTool(ctx, p)
		if err != nil {
			return err
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if len(b) > MaxResult {
			return ErrTooLarge
		}
		res = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// session opens a session with bearer, runs do within timeout, and closes
// it. An error is reduced to its class.
func (c *Client) session(ctx context.Context, t Target, bearer string, timeout time.Duration, do func(context.Context, *sdk.ClientSession) error) error {
	tr := &callTransport{bearer: bearer, next: http.DefaultTransport}
	if c.HTTP != nil && c.HTTP.Transport != nil {
		tr.next = c.HTTP.Transport
	}
	hc := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return httpx.ErrRedirect }}
	cl := sdk.NewClient(&sdk.Implementation{Name: model.ProductName, Version: "v1alpha1"}, nil)
	cctx, cancel := context.WithTimeout(ctx, or(c.ConnectTimeout, ConnectTimeout))
	cs, err := cl.Connect(cctx, &sdk.StreamableClientTransport{
		Endpoint: t.URL, HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: MaxBody,
	}, nil)
	cancel()
	if err != nil {
		return tr.class(cctx, err)
	}
	// Closing ends the session at the server (a DELETE, which the SDK
	// bounds at 5 s); the answer does not wait for it.
	defer func() { go cs.Close() }()
	octx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := do(octx, cs); err != nil {
		return tr.class(octx, err)
	}
	return nil
}

// callTransport adds the bearer to every request, caps every answer's body
// and remembers what went wrong at the HTTP layer.
type callTransport struct {
	bearer       string
	next         http.RoundTripper
	unauthorized atomic.Bool
	tooLarge     atomic.Bool
}

func (t *callTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.bearer)
	resp, err := t.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		t.unauthorized.Store(true)
	}
	resp.Body = &watchedBody{ReadCloser: httpx.LimitBody(resp.Body, MaxBody), over: &t.tooLarge}
	return resp, nil
}

type watchedBody struct {
	io.ReadCloser
	over *atomic.Bool
}

func (w *watchedBody) Read(p []byte) (int, error) {
	n, err := w.ReadCloser.Read(p)
	if errors.Is(err, httpx.ErrBodyTooLarge) {
		w.over.Store(true)
	}
	return n, err
}

// class is err reduced to one of the errors above. The original is not
// kept: it may quote what the connector sent.
func (t *callTransport) class(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrTooLarge), t.tooLarge.Load(), errors.Is(err, httpx.ErrBodyTooLarge):
		return ErrTooLarge
	case t.unauthorized.Load():
		return ErrUnauthorized
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return ErrTimeout
	case errors.Is(err, httpx.ErrBlockedAddress), errors.Is(err, httpx.ErrNotHTTPS), errors.Is(err, httpx.ErrRedirect):
		return fmt.Errorf("%w: %w", ErrUpstream, unwrapGuard(err))
	}
	return ErrUpstream
}

// unwrapGuard is the guard's own error inside err.
func unwrapGuard(err error) error {
	for _, g := range []error{httpx.ErrBlockedAddress, httpx.ErrNotHTTPS, httpx.ErrRedirect} {
		if errors.Is(err, g) {
			return g
		}
	}
	return err
}
