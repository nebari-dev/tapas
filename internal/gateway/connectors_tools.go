//go:build !local

package gateway

import (
	"context"
	"net/http"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/client"
	gwconnectors "github.com/nebari-dev/javo/internal/gateway/connectors"
	"github.com/nebari-dev/javo/internal/httpx"
)

// connectorHTTP is the client connectors are reached with: the configured
// one, or a guarded one that allows no internal host.
func connectorHTTP(cfg Config) *http.Client {
	if cfg.ConnectorHTTP != nil {
		return cfg.ConnectorHTTP
	}
	return httpx.Guard{PublicOnly: true}.Client()
}

// connectorTools lists connectors' tools for the admins' snapshot.
func connectorTools(cfg Config) gwconnectors.ToolLister {
	return toolLister{&client.Client{HTTP: connectorHTTP(cfg)}}
}

type toolLister struct{ c *client.Client }

func (l toolLister) ListTools(ctx context.Context, c connectors.Connector, bearer string) ([]connectors.Tool, error) {
	tools, err := l.c.ListTools(ctx, client.Target{URL: c.URL}, bearer)
	if err != nil {
		return nil, err
	}
	out := make([]connectors.Tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, connectors.Tool{Name: t.Name, Description: t.Description, ReadOnlyHint: t.Annotations != nil && t.Annotations.ReadOnlyHint})
	}
	return out, nil
}
