package broker

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/reasons"
)

// identify refuses a caller whose token may not use connectors (D15) and
// splits the tool's name. A name longer than any Tapas offers is unknown,
// so the audit row never holds an overlong name an agent made up.
func (b *Broker) identify(_ context.Context, c *call) *sdk.CallToolResult {
	slug, tool, ok := connectors.SplitTool(c.name)
	if ok && len(c.name) <= connectors.MaxToolName {
		c.conn.Slug, c.tool = slug, tool
	}
	if !c.caller.MayUseConnectors() {
		return errorResult(c, outcomeDeniedToken, reasons.Msg(reasons.MsgConnectorNotForAppTokens))
	}
	if c.tool == "" {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	return nil
}

// noSuchTool is the one answer for an unknown connector or tool and for one
// the caller may not use, so a caller cannot tell them apart.
func noSuchTool(c *call, outcome string) *sdk.CallToolResult {
	return errorResult(c, outcome, reasons.Msg(reasons.MsgConnectorToolNotFound))
}
