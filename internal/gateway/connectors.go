//go:build !local

package gateway

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/gateway/connectors"
)

// mountConnectors registers the connector routes. They are always there
// (the OpenAPI document lists them); with connectors off they answer 409.
// A binary built with the tag local carries none (connectors_local.go).
func mountConnectors(api huma.API, cfg Config) {
	(&connectors.API{Registry: cfg.Connectors, Roles: cfg.Roles, PublicURL: cfg.MCP.PublicURL}).Register(api)
}
