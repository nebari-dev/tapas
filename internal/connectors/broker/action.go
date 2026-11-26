package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/plans"
)

// argumentsParam is the proposal's parameter that holds the call's
// arguments, a JSON object.
const argumentsParam = "arguments"

// Action is the connector-call catalog action (D18): planning a write
// repeats the call's checks and asks the policy, and applying an approved
// one checks again and forwards exactly the stored arguments once.
// callerOf is who the API request in ctx comes from: the agent's token
// when it proposes, the owner's browser session when they approve.
func (b *Broker) Action(callerOf func(context.Context) connectors.Caller) actions.Action {
	return &action{b: b, callerOf: callerOf}
}

type action struct {
	b        *Broker
	callerOf func(context.Context) connectors.Caller
}

func (a *action) Info() actions.Info {
	return actions.Info{ID: actions.ConnectorCall, Summary: "Call a connector's tool that changes things", Hidden: true}
}

// Plan checks p as a call by the person in ctx and shows each top-level
// argument as it will be sent. The arguments are stored with insignificant
// space removed and are otherwise kept byte for byte, in their order; an
// object naming a field twice is refused, since the review could not show
// which one the connector takes.
func (a *action) Plan(ctx context.Context, p actions.Proposal) (actions.Planned, error) {
	c := a.newCall(ctx, phasePlan, p)
	res := a.b.run(ctx, c)
	if res == nil && p.Owner != "" && p.Owner != c.caller.Principal.ID {
		res = noSuchTool(c, outcomeDeniedAuthz)
	}
	var changes []actions.Change
	if res == nil {
		var ok bool
		if changes, ok = changesOf(c.args); !ok {
			res = errorResult(c, outcomeToolError, "Give each argument once, in a JSON object of at most 64 KiB.")
		}
	}
	if r, ok := ctx.Value(planReportKey{}).(*planReport); ok {
		r.outcome, r.policy = c.outcome, c.policy
	}
	if res != nil {
		return actions.Planned{}, refusal(c, res)
	}
	var args bytes.Buffer
	_ = json.Compact(&args, c.args) // checked by resolve
	return actions.Planned{
		Proposal: actions.Proposal{
			Action: actions.ConnectorCall, Target: p.Target, Owner: c.caller.Principal.ID,
			Params: map[string]string{argumentsParam: args.String()},
		},
		TargetName: c.conn.Name, Summary: c.conn.Name + ": " + c.tool, Changes: changes,
		Destructive: destructive(c.listed),
	}, nil
}

// Apply makes the approved call p for its owner: the checks again, the
// policy in the apply phase (anything but allow refuses), the connection's
// token, then the stored arguments forwarded once. Its audit row names the
// plan. A tool error from the connector fails the plan.
func (a *action) Apply(ctx context.Context, p actions.Proposal) error {
	c := a.newCall(ctx, phaseApply, p)
	c.plan = plans.ApplyingID(ctx)
	res := a.b.identify(ctx, c)
	if res == nil && p.Owner != c.caller.Principal.ID {
		res = noSuchTool(c, outcomeDeniedAuthz)
	}
	if res == nil {
		res = a.b.run(ctx, c)
	}
	// The connector may have acted: the outcome stands even when its row
	// cannot be written (appendRow logs that).
	_ = a.b.appendRow(ctx, c)
	switch c.outcome {
	case outcomeOK:
		return nil
	case outcomeToolError:
		// The connector's own words could hold anything: not passed on.
		return problem.New(http.StatusUnprocessableEntity, "The connector answered that the call failed.")
	}
	return refusal(c, res)
}

func (a *action) newCall(ctx context.Context, ph phase, p actions.Proposal) *call {
	return &call{
		phase: ph, caller: a.callerOf(ctx), name: p.Target, args: json.RawMessage(p.Params[argumentsParam]), started: a.b.c.Now(),
	}
}

// destructive reads t's hints as the MCP spec defines them: a tool that only
// reads is not destructive, any other one is unless it says it is not. The
// hints come from the connector, so a missing one errs towards asking again.
func destructive(t *sdk.Tool) bool {
	a := t.Annotations
	if a == nil {
		return true
	}
	if a.ReadOnlyHint {
		return false
	}
	return a.DestructiveHint == nil || *a.DestructiveHint
}

// changesOf is one change per top-level field of the object args, its value
// as JSON without insignificant space; false when a field comes twice.
func changesOf(args json.RawMessage) ([]actions.Change, bool) {
	dec := json.NewDecoder(bytes.NewReader(args))
	if _, err := dec.Token(); err != nil { // the opening brace
		return nil, false
	}
	out := []actions.Change{}
	seen := map[string]bool{}
	for dec.More() {
		k, err := dec.Token()
		key, _ := k.(string)
		var v json.RawMessage
		if err != nil || seen[key] || dec.Decode(&v) != nil {
			return nil, false
		}
		seen[key] = true
		var to bytes.Buffer
		_ = json.Compact(&to, v)
		out = append(out, actions.Change{Field: key, To: to.String()})
	}
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	_, err := dec.Token()
	return out, err == io.EOF
}

// refusal is res, how c ended, as the API answers it.
func refusal(c *call, res *sdk.CallToolResult) error {
	text := serverError
	if res != nil && len(res.Content) == 1 {
		if t, ok := res.Content[0].(*sdk.TextContent); ok {
			text = t.Text
		}
	}
	status := http.StatusBadGateway
	switch c.outcome {
	case outcomeDeniedToken, outcomeDeniedPolicy:
		status = http.StatusForbidden
	case outcomeDeniedAuthz:
		status = http.StatusNotFound
	case outcomeNotConnected:
		status = http.StatusConflict
	case outcomeToolError:
		status = http.StatusUnprocessableEntity
	}
	return problem.New(status, text)
}
