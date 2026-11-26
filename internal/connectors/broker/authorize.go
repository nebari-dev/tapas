package broker

import (
	"context"
	"errors"
	"log/slog"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/store"
)

// authorize finds the connector by its slug and asks authz whether the
// caller may use it. An unknown, disabled or forbidden connector answers
// as an unknown tool.
func (b *Broker) authorize(ctx context.Context, c *call) *sdk.CallToolResult {
	conn, err := b.c.Connectors.BySlug(ctx, c.conn.Slug)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return noSuchTool(c, outcomeDeniedAuthz)
	case err != nil:
		slog.Error("read a connector", "connector", c.conn.Slug, "err", err)
		return errorResult(c, outcomeUpstreamError, serverError)
	}
	c.conn = conn
	if !allowed(c.caller, conn) {
		return noSuchTool(c, outcomeDeniedAuthz)
	}
	return nil
}

// allowed is whether caller may use conn.
func allowed(caller connectors.Caller, conn connectors.Connector) bool {
	return authz.Decide(caller.Principal, caller.Roles, authz.ActUseConnector, authz.Object{
		Connector: &authz.ConnectorAsk{Enabled: conn.Enabled, Groups: conn.AllowedGroups},
	}).Allowed
}
