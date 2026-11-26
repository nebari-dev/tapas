//go:build local

package gateway

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/buildinfo"
	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// mountMCP is the agent endpoint of a binary built with the tag local:
// none. `javo local` never turns MCP on, so the protocol's library (about
// 1 MB) is not carried, and the server refuses a config that turns it on
// before a gateway is built. Like a gateway built without a store, one
// built with it anyway does not start.
func mountMCP(*http.ServeMux, *huma.OpenAPI, []actions.Info, *authn.Authenticator, Config) {
	panic(buildinfo.LeftOut("the MCP endpoint (mcp.enabled)"))
}
