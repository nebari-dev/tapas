// Package broker puts connectors' tools on Tapas's MCP endpoint. A call
// passes, in order: identify (who calls, from the verified Tapas token),
// authorize (authz), the tool and its arguments, checkPolicy (the policy
// hook), propose (writes become plans), token (the connection's bearer),
// forward (to the connector) and record (the audit row). Each step is a
// method in its own file.
package broker

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/client"
	"github.com/nebari-dev/javo/internal/connectors/policy"
)

// Connectors is where the broker reads connectors (the registry).
type Connectors interface {
	ListEnabled(ctx context.Context) ([]connectors.Connector, error)
	BySlug(ctx context.Context, slug string) (connectors.Connector, error)
}

// Config wires a Broker. Connectors and Forwarder are required.
type Config struct {
	Connectors Connectors
	// Forwarder reaches the connectors: the client over the guarded HTTP
	// client in production.
	Forwarder client.Forwarder
	// Tokens gives each call its bearer; nil means nobody is connected.
	Tokens connectors.TokenSource
	// Policy is asked before a call goes ahead; nil allows all.
	Policy policy.Checker
	// PublicURL is where people reach Tapas, for the page they connect
	// accounts at.
	PublicURL string
	// Now is the clock; nil is the real one.
	Now func() time.Time
}

// Broker lists and calls connectors' tools for a caller.
type Broker struct {
	c     Config
	cache *cache
}

// New returns a Broker over c.
func New(c Config) *Broker {
	if c.Policy == nil {
		c.Policy = policy.AllowAll{}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Broker{c: c, cache: newCache(c.Now)}
}

// MaxArguments is the largest arguments object a call may carry.
const MaxArguments = 64 << 10

// call is one tool call as it passes the steps.
type call struct {
	caller connectors.Caller
	name   string // as the agent gave it: slug__tool
	tool   string // the connector's own name for it
	args   json.RawMessage
	conn   connectors.Connector
	mode   connectors.ToolMode
	bearer string
	// outcome is how the call ended, for the audit row.
	outcome string
	started time.Time
}

// Call runs a connector's tool for caller. It answers false, and nothing
// else, when name is not a connector's tool (it holds no separator); any
// other name is answered here, never by Tapas's own tools.
func (b *Broker) Call(ctx context.Context, caller connectors.Caller, name string, args json.RawMessage) (*sdk.CallToolResult, bool) {
	if !strings.Contains(name, connectors.ToolSeparator) {
		return nil, false
	}
	c := &call{caller: caller, name: name, args: args, started: b.c.Now()}
	return b.record(ctx, c, b.run(ctx, c)), true
}

// run passes c through the steps; the first that answers ends it.
func (b *Broker) run(ctx context.Context, c *call) *sdk.CallToolResult {
	steps := []func(context.Context, *call) *sdk.CallToolResult{
		b.identify, b.authorize, b.token, b.resolve, b.checkPolicy, b.propose, b.forward,
	}
	for _, step := range steps {
		if res := step(ctx, c); res != nil {
			return res
		}
	}
	return errorResult(c, outcomeUpstreamError, "The connector gave no answer.")
}

// subject is whose connection a call to conn uses: the person's own, or
// the company-wide one.
func subject(caller connectors.Caller, conn connectors.Connector) string {
	if conn.AuthMode == connectors.AuthShared {
		return ""
	}
	return caller.Principal.ID
}

// errorResult is a tool error with text, ending c with outcome.
func errorResult(c *call, outcome, text string) *sdk.CallToolResult {
	c.outcome = outcome
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}
