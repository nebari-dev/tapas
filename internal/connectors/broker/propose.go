package broker

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
)

// propose turns a write into a plan its owner approves in the browser.
// Until plans can carry connector calls, a write is refused and never
// reaches the connector.
func (b *Broker) propose(_ context.Context, c *call) *sdk.CallToolResult {
	if c.mode == connectors.ToolRead {
		return nil
	}
	return errorResult(c, outcomeWriteUnavailable, "This tool changes things at the connector, and changes through connectors cannot be proposed yet. Only read tools can be called.")
}
