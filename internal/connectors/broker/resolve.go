package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
)

// resolve checks the tool is one the connector lists and that is not off,
// takes its mode from the admin's modes (never from the connector), and
// checks the arguments are a JSON object of at most MaxArguments bytes.
func (b *Broker) resolve(ctx context.Context, c *call) *sdk.CallToolResult {
	c.mode = connectors.ModeOf(c.conn.ToolModes, c.tool)
	if c.mode == connectors.ToolOff {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	tools, err := b.upstream(ctx, c.caller, c.conn, c.bearer)
	if err != nil {
		return errorResult(c, outcomeOf(err), upstreamText(err))
	}
	i := slices.IndexFunc(tools, func(t *sdk.Tool) bool { return t.Name == c.tool })
	if i < 0 {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	c.listed = tools[i]
	args := bytes.TrimSpace(c.args)
	if len(args) == 0 {
		args = []byte("{}")
	}
	var obj map[string]json.RawMessage
	if len(args) > MaxArguments || args[0] != '{' || json.Unmarshal(args, &obj) != nil {
		return errorResult(c, outcomeToolError, "Give the arguments as a JSON object of at most 64 KiB.")
	}
	c.args = args
	return nil
}
