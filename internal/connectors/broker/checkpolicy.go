package broker

import (
	"context"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/policy"
	"github.com/nebari-dev/javo/internal/reasons"
)

// checkPolicy asks the policy hook about a call. An agent's read is asked
// in the read phase, and require_approval makes it a write; an agent's
// write is asked when it is planned. A plan may be made when the policy
// allows it or asks for approval; an approved plan is applied only when
// the policy allows it then. A deny ends the call with the policy's
// reason.
func (b *Broker) checkPolicy(ctx context.Context, c *call) *sdk.CallToolResult {
	ph := map[phase]policy.Phase{phaseCall: policy.PhaseRead, phasePlan: policy.PhasePropose, phaseApply: policy.PhaseApply}[c.phase]
	if c.phase == phaseCall && c.mode != connectors.ToolRead {
		return nil
	}
	d := policy.Evaluate(ctx, b.c.Policy, policy.Input{
		Phase: ph, Person: c.caller.Principal.ID, Groups: c.caller.Principal.Groups, Agent: c.caller.Agent,
		TokenID: c.caller.Token.ID, Connector: c.conn.Slug, Tool: c.tool, Write: c.mode == connectors.ToolWrite, Arguments: c.args,
	})
	c.policy = d.Effect
	switch {
	case d.Effect == policy.Allow:
		return nil
	case d.Effect == policy.RequireApproval && c.phase == phaseCall:
		c.mode = connectors.ToolWrite
		return nil
	case d.Effect == policy.RequireApproval && c.phase == phasePlan:
		return nil
	case d.Effect == policy.RequireApproval:
		d.Reason = "it asks for approval again when the change is made"
	}
	return errorResult(c, outcomeDeniedPolicy, reasons.Msg(reasons.MsgConnectorDeniedByPolicy, strings.TrimRight(d.Reason, ".")))
}
