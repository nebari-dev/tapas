package broker

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// How a call ended, for its audit row.
const (
	outcomeOK            = "ok"
	outcomeToolError     = "tool_error"
	outcomeDeniedToken   = "denied_token"
	outcomeDeniedAuthz   = "denied_authz"
	outcomeDeniedPolicy  = "denied_policy"
	outcomeNotConnected  = "not_connected"
	outcomeUpstreamError = "upstream_error"
	outcomeTimeout       = "timeout"
	// outcomeWriteUnavailable is a write refused while writes cannot be
	// proposed yet; proposing replaces it with "proposed".
	outcomeWriteUnavailable = "write_unavailable"
)

// record writes the call's audit row once its outcome is known and returns
// the answer to give. Nothing is recorded yet.
func (b *Broker) record(_ context.Context, _ *call, res *sdk.CallToolResult) *sdk.CallToolResult {
	return res
}
