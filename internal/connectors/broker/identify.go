package broker

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/reasons"
)

// identify refuses a caller whose token may not use connectors (D15) and
// splits the tool's name.
func (b *Broker) identify(_ context.Context, c *call) *sdk.CallToolResult {
	if !c.caller.MayUseConnectors() {
		return errorResult(c, outcomeDeniedToken, reasons.Msg(reasons.MsgConnectorNotForAppTokens))
	}
	slug, tool, ok := connectors.SplitTool(c.name)
	if !ok {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	c.conn.Slug, c.tool = slug, tool
	return nil
}

// noSuchTool is the one answer for an unknown connector or tool and for one
// the caller may not use, so a caller cannot tell them apart.
func noSuchTool(c *call, outcome string) *sdk.CallToolResult {
	return errorResult(c, outcome, reasons.Msg(reasons.MsgConnectorToolNotFound))
}
