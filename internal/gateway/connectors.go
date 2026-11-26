package gateway

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/connectors"
)

// mountConnectors registers the connector routes. They are there in every
// build, so the OpenAPI document is the same for `javo local` and a cluster;
// with connectors off (always so in `javo local`) they answer 409.
func mountConnectors(api huma.API, cfg Config) {
	(&connectors.API{Registry: cfg.Connectors, Roles: cfg.Roles, PublicURL: cfg.MCP.PublicURL}).Register(api)
}
