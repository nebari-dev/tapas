package broker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/policy"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
)

// propose turns a write into a plan its owner approves in the browser,
// through the plans route with the caller's own credentials (the
// connector-call action checks it there again). Nothing is forwarded: the
// agent gets the plan's id and the page to approve it at.
func (b *Broker) propose(ctx context.Context, c *call) *sdk.CallToolResult {
	if c.mode == connectors.ToolRead {
		return nil
	}
	if !c.caller.Token.Scope.Allows(http.MethodPost, token.PlansRoute) {
		return errorResult(c, outcomeDeniedToken, reasons.Msg(reasons.MsgTokenReadOnly))
	}
	if c.caller.Propose == nil {
		slog.Error("a connector write has no way to be proposed", "connector", c.conn.Slug)
		return errorResult(c, outcomeUpstreamError, serverError)
	}
	// The action reports how its own checks ended, for this call's row.
	planned := &planReport{}
	p, err := c.caller.Propose(context.WithValue(ctx, planReportKey{}, planned), actions.Proposal{
		Action: actions.ConnectorCall, Target: c.name, Params: map[string]string{argumentsParam: string(c.args)},
	})
	if planned.policy != "" {
		c.policy = planned.policy
	}
	var refused *connectors.ProposalRefused
	switch {
	case errors.As(err, &refused):
		outcome := planned.outcome
		if outcome == "" {
			outcome = outcomeToolError
		}
		return errorResult(c, outcome, refused.Text)
	case err != nil:
		slog.Error("propose a connector call", "connector", c.conn.Slug, "err", err)
		return errorResult(c, outcomeUpstreamError, serverError)
	}
	c.outcome, c.plan = outcomeProposed, plans.StoredID(p.ID)
	out := map[string]any{"planId": p.ID, "state": "awaiting_approval", "approveUrl": p.ApproveURL}
	text, _ := json.Marshal(out) // strings only: cannot fail
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(text)}}, StructuredContent: out}
}

// planReport is how planning a call proposed by propose ended: its outcome
// when the checks refused it, and the policy's effect when it was asked.
type planReport struct {
	outcome string
	policy  policy.Effect
}

type planReportKey struct{}
