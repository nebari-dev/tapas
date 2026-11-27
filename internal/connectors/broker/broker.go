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
	"github.com/nebari-dev/javo/internal/store"
)

// Connectors is where the broker reads connectors (the registry).
type Connectors interface {
	ListEnabled(ctx context.Context) ([]connectors.Connector, error)
	BySlug(ctx context.Context, slug string) (connectors.Connector, error)
}

// Auditor appends audit rows: the store's audit log in production.
type Auditor interface {
	Append(ctx context.Context, e store.AuditEvent) error
}

// Config wires a Broker. Connectors, Forwarder and Audit are required.
type Config struct {
	Connectors Connectors
	// Forwarder reaches the connectors: the client over the guarded HTTP
	// client in production.
	Forwarder client.Forwarder
	// Tokens gives each call its bearer; nil means nobody is connected.
	Tokens connectors.TokenSource
	// Audit records one row per tool call (D19); a call whose row cannot be
	// written answers an error. Required: without it every call fails.
	Audit Auditor
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
	// outcome is how the call ended, and policy the policy's effect (empty
	// when it was not asked), for the audit row.
	outcome string
	policy  policy.Effect
	started time.Time
	// phase is what the steps run for: an agent's call, planning a write,
	// or applying an approved one.
	phase phase
	// listed is the connector's own entry for the tool, once resolved.
	listed *sdk.Tool
	// plan is the stored id of the plan the call became or applies.
	plan string
	// approved is the connector an approved plan was asked for; applying
	// refuses any other one.
	approved approvedConnector
}

// approvedConnector pins a plan to a connector by id and address.
type approvedConnector struct{ ID, URL string }

// phase is what a call's steps run for.
type phase int

const (
	phaseCall  phase = iota // an agent calls a tool through /mcp
	phasePlan               // a write is checked to become a plan
	phaseApply              // an approved plan is made
)

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

// run passes c through the steps of its phase; the first that answers ends
// it. Planning ends after the checks, with nil when they all passed.
func (b *Broker) run(ctx context.Context, c *call) *sdk.CallToolResult {
	steps := []func(context.Context, *call) *sdk.CallToolResult{b.identify, b.authorize, b.token, b.resolve, b.checkPolicy}
	switch c.phase {
	case phasePlan:
	case phaseApply:
		steps = append(steps, b.forward)
	default:
		steps = append(steps, b.propose, b.forward)
	}
	for _, step := range steps {
		if res := step(ctx, c); res != nil {
			return res
		}
	}
	if c.phase == phasePlan {
		return nil
	}
	return errorResult(c, outcomeUpstreamError, "The connector gave no answer.")
}

// errorResult is a tool error with text, ending c with outcome.
func errorResult(c *call, outcome, text string) *sdk.CallToolResult {
	c.outcome = outcome
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}
