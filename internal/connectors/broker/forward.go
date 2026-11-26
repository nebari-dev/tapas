package broker

import (
	"context"
	"errors"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/client"
	"github.com/nebari-dev/javo/internal/reasons"
)

// invalidator is a token source that can replace a token a connector
// refused (the vault).
type invalidator interface {
	Invalidate(ctx context.Context, c connectors.Connector, subject, refused string) error
}

// forward calls the connector's tool with the arguments unchanged and
// answers its result as it came (the client caps its size). When the
// connector refuses the token, a token source that can replace it is asked
// to, and the call is tried once more.
func (b *Broker) forward(ctx context.Context, c *call) *sdk.CallToolResult {
	res, err := b.c.Forwarder.CallTool(ctx, client.Target{URL: c.conn.URL}, c.bearer, c.tool, c.args)
	if inv, ok := b.c.Tokens.(invalidator); ok && errors.Is(err, client.ErrUnauthorized) {
		if ierr := inv.Invalidate(ctx, c.conn, subject(c.caller, c.conn), c.bearer); ierr == nil {
			if bearer, terr := b.bearer(ctx, c.caller, c.conn); terr == nil {
				c.bearer = bearer
				res, err = b.c.Forwarder.CallTool(ctx, client.Target{URL: c.conn.URL}, c.bearer, c.tool, c.args)
			}
		}
	}
	if err != nil {
		if errors.Is(err, client.ErrUnauthorized) {
			return errorResult(c, outcomeNotConnected, b.notConnected(c.conn))
		}
		return errorResult(c, outcomeOf(err), upstreamText(err))
	}
	c.outcome = outcomeOK
	if res.IsError {
		c.outcome = outcomeToolError
	}
	return res
}

// serverError is the answer when Tapas itself failed.
var serverError = reasons.Msg(reasons.MsgServerError)

// outcomeOf is the audit outcome of a client error.
func outcomeOf(err error) string {
	if errors.Is(err, client.ErrTimeout) {
		return outcomeTimeout
	}
	return outcomeUpstreamError
}

// upstreamText tells the agent what went wrong reaching a connector, by
// class only: the connector's own words are not passed on.
func upstreamText(err error) string {
	switch {
	case errors.Is(err, client.ErrTimeout):
		return "The connector did not answer in time."
	case errors.Is(err, client.ErrTooLarge):
		return reasons.Msg(reasons.MsgToolAnswerTooLarge, client.MaxResult>>20)
	}
	return "The connector could not be reached or failed. Try again later."
}
