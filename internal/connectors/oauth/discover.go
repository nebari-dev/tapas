package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
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

// EndpointsTTL is how long a discovery is kept before Start reads the MCP
// server's metadata again to see whether it still names the pinned
// authorization server.
const EndpointsTTL = time.Hour

type cachedServer struct {
	s  connectors.AuthServer
	at time.Time
}

// pinnedEndpoints are c's static endpoints when it has them, else those of the
// authorization server pinned on it. Without either it is CodeDiscovery:
// only Start discovers, and it pins what it found. Finishing a sign-in,
// refreshing and revoking never follow a later discovery, so a server the
// metadata names afterwards never gets a code, a token or the client
// secret.
func pinnedEndpoints(c connectors.Connector) (endpoints, error) {
	if c.OAuth.AuthorizeURL != "" {
		return endpoints{authorize: c.OAuth.AuthorizeURL, token: c.OAuth.TokenURL, revoke: c.OAuth.RevokeURL}, nil
	}
	p := c.OAuth.Server
	if p == nil {
		return endpoints{}, &connectors.OAuthError{Code: connectors.CodeDiscovery}
	}
	return endpoints{
		authorize: p.AuthorizeURL, token: p.TokenURL, revoke: p.RevokeURL, register: p.RegistrationURL,
		issuer: p.Issuer, issParam: p.IssParameter, resource: c.URL, authMethods: p.TokenAuthMethods,
	}, nil
}

// startEndpoints are where a new sign-in to c goes, and c as it is then.
// The first one discovers the authorization server and pins it on c (actor
// is on the audit log for that). Later ones read the metadata again (at
// most once per EndpointsTTL): when it names another issuer, c is marked
// moved and it is connectors.ErrServerMoved until an admin saves c again.
// A failed re-read keeps the pinned server.
func (s *Service) startEndpoints(ctx context.Context, actor model.Actor, c connectors.Connector) (connectors.Connector, endpoints, error) {
	if c.OAuth.AuthorizeURL != "" {
		e, err := pinnedEndpoints(c)
		return c, e, err
	}
	if c.OAuth.Server == nil {
		found, err := s.discovered(ctx, c)
		if err != nil {
			return c, endpoints{}, err
		}
		got, err := s.c.Clients.Pin(ctx, actor, c, found)
		if errors.Is(err, store.ErrConflict) {
			// Changed or pinned meanwhile: use what is stored now.
			if got, err = s.c.Clients.Get(ctx, c.ID); err == nil && got.OAuth.Server == nil && got.OAuth.AuthorizeURL == "" {
				err = connectors.ErrSignInExpired
			}
		}
		if err != nil {
			return c, endpoints{}, err
		}
		if c = got; c.OAuth.AuthorizeURL != "" {
			e, err := pinnedEndpoints(c)
			return c, e, err
		}
	}
	if c.OAuth.Server.Moved {
		return c, endpoints{}, connectors.ErrServerMoved
	}
	if found, err := s.discovered(ctx, c); err == nil && found.Issuer != c.OAuth.Server.Issuer {
		if err := s.c.Clients.ServerMoved(ctx, model.Actor{System: model.SystemGateway}, c); err != nil {
			return c, endpoints{}, err
		}
		return c, endpoints{}, connectors.ErrServerMoved
	}
	e, err := pinnedEndpoints(c)
	return c, e, err
}

// discovered is the authorization server c's MCP server names, kept for
// EndpointsTTL per connector and address.
func (s *Service) discovered(ctx context.Context, c connectors.Connector) (connectors.AuthServer, error) {
	key := c.ID + "\x00" + c.URL
	now := s.c.Now()
	s.mu.Lock()
	hit, ok := s.cache[key]
	s.mu.Unlock()
	if ok && now.Sub(hit.at) < EndpointsTTL {
		return hit.s, nil
	}
	found, err := s.discover(ctx, c.URL)
	if err != nil {
		return connectors.AuthServer{}, err
	}
	s.mu.Lock()
	for k, v := range s.cache {
		if now.Sub(v.at) >= EndpointsTTL {
			delete(s.cache, k)
		}
	}
	s.cache[key] = cachedServer{s: found, at: now}
	s.mu.Unlock()
	return found, nil
}

// discover finds the authorization server of the MCP server at mcpURL:
// RFC 9728 metadata (from the WWW-Authenticate hint, else the well-known
// paths) whose resource is exactly mcpURL, then the server's RFC 8414 or
// OpenID metadata. A server without PKCE S256 is refused.
func (s *Service) discover(ctx context.Context, mcpURL string) (connectors.AuthServer, error) {
	ctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	failed := &connectors.OAuthError{Code: connectors.CodeDiscovery}
	prm := s.resourceMetadata(ctx, mcpURL)
	if prm == nil || len(prm.AuthorizationServers) == 0 {
		return connectors.AuthServer{}, failed
	}
	issuer := prm.AuthorizationServers[0]
	if connectors.CheckURL(issuer, s.c.InternalHosts) != nil {
		return connectors.AuthServer{}, failed
	}
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, s.c.HTTP)
	if err != nil || asm == nil || asm.Issuer == "" || asm.AuthorizationEndpoint == "" || asm.TokenEndpoint == "" {
		return connectors.AuthServer{}, failed
	}
	if !slices.Contains(asm.CodeChallengeMethodsSupported, "S256") {
		return connectors.AuthServer{}, &connectors.OAuthError{Code: connectors.CodeNoPKCE}
	}
	for _, u := range []string{asm.AuthorizationEndpoint, asm.TokenEndpoint, asm.RevocationEndpoint, asm.RegistrationEndpoint} {
		if u != "" && connectors.CheckURL(u, s.c.InternalHosts) != nil {
			return connectors.AuthServer{}, failed
		}
	}
	return connectors.AuthServer{
		Issuer: asm.Issuer, AuthorizeURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint,
		RevokeURL: asm.RevocationEndpoint, RegistrationURL: asm.RegistrationEndpoint,
		IssParameter: asm.AuthorizationResponseIssParameterSupported, TokenAuthMethods: asm.TokenEndpointAuthMethodsSupported,
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
