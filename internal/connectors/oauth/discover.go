package oauth

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/nebari-dev/javo/internal/connectors"
)

// endpoints are where a connector's people sign in and what its
// authorization server supports.
type endpoints struct {
	authorize, token, revoke, register string
	// issuer names itself on the redirect when issParam (RFC 9207).
	issuer   string
	issParam bool
	// resource is sent as RFC 8707's resource parameter: the MCP URL when
	// the endpoints were discovered, "" for static ones.
	resource string
	// authMethods are the token endpoint's client authentication methods.
	authMethods []string
}

// EndpointsTTL is how long discovered endpoints are kept, per connector
// revision.
const EndpointsTTL = time.Hour

type cachedEndpoints struct {
	e  endpoints
	at time.Time
}

// endpoints are c's: its static ones when it has them, else discovered
// from its MCP server (and kept for EndpointsTTL per revision).
func (s *Service) endpoints(ctx context.Context, c connectors.Connector) (endpoints, error) {
	if c.OAuth.AuthorizeURL != "" {
		return endpoints{authorize: c.OAuth.AuthorizeURL, token: c.OAuth.TokenURL, revoke: c.OAuth.RevokeURL}, nil
	}
	key := c.ID + "\x00" + c.Rev
	now := s.c.Now()
	s.mu.Lock()
	hit, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Sub(hit.at) < EndpointsTTL {
		return hit.e, nil
	}
	e, err := s.discover(ctx, c.URL)
	if err != nil {
		return endpoints{}, err
	}
	s.mu.Lock()
	for k, v := range s.cache {
		if now.Sub(v.at) >= EndpointsTTL {
			delete(s.cache, k)
		}
	}
	s.cache[key] = cachedEndpoints{e: e, at: now}
	s.mu.Unlock()
	return e, nil
}

// discover finds the authorization server of the MCP server at mcpURL:
// RFC 9728 metadata (from the WWW-Authenticate hint, else the well-known
// paths) whose resource is exactly mcpURL, then the server's RFC 8414 or
// OpenID metadata. A server without PKCE S256 is refused.
func (s *Service) discover(ctx context.Context, mcpURL string) (endpoints, error) {
	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	failed := &connectors.OAuthError{Code: connectors.CodeDiscovery}
	prm := s.resourceMetadata(ctx, mcpURL)
	if prm == nil || len(prm.AuthorizationServers) == 0 {
		return endpoints{}, failed
	}
	issuer := prm.AuthorizationServers[0]
	if connectors.CheckURL(issuer, s.c.InternalHosts) != nil {
		return endpoints{}, failed
	}
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, s.c.HTTP)
	if err != nil || asm == nil || asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return endpoints{}, failed
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return endpoints{}, &connectors.OAuthError{Code: connectors.CodeNoPKCE}
	}
	for _, u := range []string{asm.AuthorizationEndpoint, asm.TokenEndpoint, asm.RevocationEndpoint, asm.RegistrationEndpoint} {
		if u != "" && connectors.CheckURL(u, s.c.InternalHosts) != nil {
			return endpoints{}, failed
		}
	}
	return endpoints{
		authorize: asm.AuthorizationEndpoint, token: asm.TokenEndpoint, revoke: asm.RevocationEndpoint,
		register: asm.RegistrationEndpoint, issuer: asm.Issuer, issParam: asm.AuthorizationResponseIssParameterSupported,
		resource: mcpURL, authMethods: asm.TokenEndpointAuthMethodsSupported,
	}, nil
}

// resourceMetadata is the RFC 9728 metadata of the MCP server at mcpURL,
// or nil when none is found.
func (s *Service) resourceMetadata(ctx context.Context, mcpURL string) *oauthex.ProtectedResourceMetadata {
	for _, u := range s.metadataURLs(ctx, mcpURL) {
		if connectors.CheckURL(u, s.c.InternalHosts) != nil {
			continue
		}
		// It checks that the metadata's resource is exactly mcpURL, so a
		// token is asked for the address it will be sent to.
		if prm, err := oauthex.GetProtectedResourceMetadata(ctx, u, mcpURL, s.c.HTTP); err == nil {
			return prm
		}
	}
	return nil
}

// metadataURLs are where the MCP server's metadata may be: the address its
// unauthenticated answer names, then the well-known path with the MCP
// path appended, then the well-known path at the root.
func (s *Service) metadataURLs(ctx context.Context, mcpURL string) []string {
	var out []string
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, mcpURL, nil); err == nil {
		req.Header.Set("Accept", "application/json, text/event-stream")
		if res, err := s.c.HTTP.Do(req); err == nil {
			_ = res.Body.Close()
			if cs, err := oauthex.ParseWWWAuthenticate(res.Header.Values("WWW-Authenticate")); err == nil {
				for _, c := range cs {
					if u := c.Params["resource_metadata"]; u != "" {
						out = append(out, u)
						break
					}
				}
			}
		}
	}
	u, err := url.Parse(mcpURL)
	if err != nil {
		return out
	}
	u.RawQuery = ""
	path := strings.TrimLeft(u.Path, "/")
	u.Path = "/.well-known/oauth-protected-resource"
	root := u.String()
	if path != "" {
		u.Path += "/" + path
		out = append(out, u.String())
	}
	return append(out, root)
}
