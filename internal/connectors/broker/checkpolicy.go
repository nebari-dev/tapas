package broker

import (
	"context"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/policy"
	"github.com/nebari-dev/javo/internal/reasons"
)

// checkPolicy asks the policy hook about a read. A deny ends the call with
// the policy's reason; require_approval makes the read a write. Writes are
// checked when they are proposed.
func (b *Broker) checkPolicy(ctx context.Context, c *call) *sdk.CallToolResult {
	if c.mode != connectors.ToolRead {
		return nil
	}
	d := policy.Evaluate(ctx, b.c.Policy, policy.Input{
		Phase: policy.PhaseRead, Person: c.caller.Principal.ID, Groups: c.caller.Principal.Groups, Agent: c.caller.Agent,
		TokenID: c.caller.Token.ID, Connector: c.conn.Slug, Tool: c.tool, Arguments: c.args,
	})
	c.policy = d.Effect
	switch d.Effect {
	case policy.Allow:
		return nil
	case policy.RequireApproval:
		c.mode = connectors.ToolWrite
		return nil
	}
	return errorResult(c, outcomeDeniedPolicy, reasons.Msg(reasons.MsgConnectorDeniedByPolicy, strings.TrimRight(d.Reason, ".")))
}
