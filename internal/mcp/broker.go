package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/reasons"
)

// Broker offers connectors' tools next to Tapas's own (Config.Broker).
type Broker interface {
	// Tools are the connector tools c may use.
	Tools(ctx context.Context, c connectors.Caller) []*sdk.Tool
	// Call runs a connector tool for c; false when name is not one.
	Call(ctx context.Context, c connectors.Caller, name string, args json.RawMessage) (*sdk.CallToolResult, bool)
}

// callerKey holds the caller in auth.TokenInfo.Extra.
const callerKey = "caller"

// SetCaller records on info who the verified token belongs to, for the
// broker. Only the bearer check calls it.
func SetCaller(info *auth.TokenInfo, c connectors.Caller) {
	if info.Extra == nil {
		info.Extra = map[string]any{}
	}
	info.Extra[callerKey] = c
}

// callerOf is the caller the bearer check recorded for req, with the MCP
// client's own name; false when there is none.
func callerOf(req sdk.Request) (connectors.Caller, bool) {
	extra := req.GetExtra()
	if extra == nil || extra.TokenInfo == nil {
		return connectors.Caller{}, false
	}
	c, ok := extra.TokenInfo.Extra[callerKey].(connectors.Caller)
	if ok {
		ss, _ := req.GetSession().(*sdk.ServerSession)
		c.Client = clientName(ss)
	}
	return c, ok
}

// proposer is how the caller of req proposes a connector call: through
// the plans route with req's own credentials, exactly as the propose tools
// do.
func (cfg Config) proposer(req *sdk.CallToolRequest) func(context.Context, actions.Proposal) (connectors.Proposed, error) {
	return func(ctx context.Context, p actions.Proposal) (connectors.Proposed, error) {
		plan, refused, err := cfg.propose(ctx, req, p)
		switch {
		case err != nil:
			return connectors.Proposed{}, err
		case refused != nil:
			return connectors.Proposed{}, &connectors.ProposalRefused{Text: refused.Content[0].(*sdk.TextContent).Text}
		}
		return connectors.Proposed{ID: plan.ID, ApproveURL: cfg.approveURL(plan.ID)}, nil
	}
}

// brokerMiddleware adds cfg.Broker's tools to the last page of tools/list, and
// sends tools/call of a name holding connectors.ToolSeparator to b, never
// to Tapas's own tools (whose names never hold it).
func brokerMiddleware(cfg Config) sdk.Middleware {
	b := cfg.Broker
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
			switch method {
			case "tools/list":
				res, err := next(ctx, method, req)
				list, ok := res.(*sdk.ListToolsResult)
				if err != nil || !ok || list.NextCursor != "" {
					return res, err
				}
				if c, ok := callerOf(req); ok {
					list.Tools = append(list.Tools, b.Tools(ctx, c)...)
				}
				return list, nil
			case "tools/call":
				call, ok := req.(*sdk.CallToolRequest)
				if !ok || call.Params == nil || !strings.Contains(call.Params.Name, connectors.ToolSeparator) {
					break
				}
				c, ok := callerOf(req)
				if !ok {
					return errorResult(reasons.Msg(reasons.MsgConnectorToolNotFound)), nil
				}
				c.Propose = cfg.proposer(call)
				if res, ok := b.Call(ctx, c, call.Params.Name, call.Params.Arguments); ok {
					return res, nil
				}
				return errorResult(reasons.Msg(reasons.MsgConnectorToolNotFound)), nil
			}
			return next(ctx, method, req)
		}
	}
}
