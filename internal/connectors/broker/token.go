package broker

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/reasons"
)

// token gets the bearer of the caller's connection to the connector.
func (b *Broker) token(ctx context.Context, c *call) *sdk.CallToolResult {
	bearer, err := b.bearer(ctx, c.caller, c.conn)
	switch {
	case errors.Is(err, connectors.ErrNotConnected):
		return errorResult(c, outcomeNotConnected, b.notConnected(c.conn))
	case err != nil:
		return errorResult(c, outcomeUpstreamError, serverError)
	}
	c.bearer = bearer
	return nil
}

// bearer is the bearer for caller's calls to conn; ErrNotConnected without
// a token source or a connection.
func (b *Broker) bearer(ctx context.Context, caller connectors.Caller, conn connectors.Connector) (string, error) {
	if b.c.Tokens == nil {
		return "", connectors.ErrNotConnected
	}
	bearer, err := b.c.Tokens.Token(ctx, conn, subject(caller, conn))
	if err != nil && !errors.Is(err, connectors.ErrNotConnected) {
		slog.Error("open a connector's token", "connector", conn.Slug, "err", err)
	}
	return bearer, err
}

// notConnected tells the caller where to connect conn.
func (b *Broker) notConnected(conn connectors.Connector) string {
	return reasons.Msg(reasons.MsgConnectorNotConnected, conn.Name, strings.TrimRight(b.c.PublicURL, "/")+connectionsPath)
}

// connectionsPath is the page of the UI people connect accounts at.
const connectionsPath = "/settings/connections"
