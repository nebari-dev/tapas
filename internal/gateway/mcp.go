//go:build !local

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/broker"
	"github.com/nebari-dev/javo/internal/connectors/client"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/mcp"
)

// liveCaller is authn.Authenticator.LiveCaller.
type liveCaller func(context.Context, string) (token.Token, authz.Principal, authn.AgentCaps, error)

// tokenVerifier checks an MCP bearer token as the API does (live reports
// whether it works now). Its scope becomes the MCP scopes: write when it may
// propose changes; the API call each tool makes checks the owner and scope
// again. Who the token belongs to is recorded for the connectors' broker.
func tokenVerifier(live liveCaller, roles config.Roles) auth.TokenVerifier {
	return func(ctx context.Context, raw string, _ *http.Request) (*auth.TokenInfo, error) {
		tok, p, caps, err := live(ctx, raw)
		if errors.Is(err, token.ErrInvalid) {
			return nil, fmt.Errorf("%w: %v", auth.ErrInvalidToken, err)
		}
		if err != nil {
			return nil, err
		}
		info := &auth.TokenInfo{UserID: tok.Owner, Scopes: []string{mcp.ScopeRead}}
		if tok.Scope.Allows(http.MethodPost, token.PlansRoute) {
			info.Scopes = append(info.Scopes, mcp.ScopeWrite)
		}
		if tok.ExpiresAt != nil {
			info.Expiration = *tok.ExpiresAt
		}
		caller := connectors.Caller{Principal: p, Roles: authz.RolesOf(p, roles), Token: tok, AgentApp: caps != (authn.AgentCaps{})}
		if caller.AgentApp {
			caller.Agent = tok.App.Short
		}
		mcp.SetCaller(info, caller)
		return info, nil
	}
}

// mountMCP serves the agent endpoint at /mcp on mux, over the API mux
// serves, and the metadata that says where it is. With connectors on, their
// tools are offered next to Tapas's own. A binary built with the tag local
// carries none of this (mcp_local.go).
func mountMCP(mux *http.ServeMux, doc *huma.OpenAPI, acts []actions.Info, a *authn.Authenticator, cfg Config) {
	var b mcp.Broker
	if cfg.Connectors != nil {
		hc := cfg.ConnectorHTTP
		if hc == nil {
			hc = httpx.Guard{PublicOnly: true}.Client()
		}
		b = broker.New(broker.Config{
			Connectors: cfg.Connectors, Forwarder: &client.Client{HTTP: hc}, Tokens: cfg.ConnectorTokens, Audit: cfg.Store.Audit(),
			PublicURL: cfg.MCP.PublicURL, Now: cfg.Now,
		})
	}
	mux.Handle(mcp.Route, mcp.NewHandler(mcp.Config{
		API: mux, Doc: doc, Actions: acts, Broker: b,
		Verify: tokenVerifier(a.LiveCaller, cfg.Roles), PublicURL: cfg.MCP.PublicURL, Version: "v1alpha1",
	}))
	mux.Handle("GET "+mcp.MetadataRoute, mcp.MetadataHandler(cfg.MCP.PublicURL))
}
