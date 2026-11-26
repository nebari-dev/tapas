// Package oauth connects people's accounts to connectors: it finds a
// connector's authorization server (RFC 9728, RFC 8414), registers Tapas
// as a client when no client was pasted and the server allows it (RFC
// 7591), starts the authorization code flow with PKCE S256 and a state
// kept hashed in the database, finishes it on any replica, refreshes
// tokens and revokes them (RFC 7009). Every request goes through the HTTP
// client it is given: the guarded one in production.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/tink-crypto/tink-go/v2/tink"
	"golang.org/x/oauth2"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Limits of a sign-in.
const (
	// StateTTL is how long a started sign-in may be finished.
	StateTTL = 10 * time.Minute
	// HTTPTimeout bounds each call to an authorization server.
	HTTPTimeout = 15 * time.Second
)

// Clients reads connectors and their OAuth clients (the registry).
type Clients interface {
	Get(ctx context.Context, id string) (connectors.Connector, error)
	ClientSecret(ctx context.Context, id string) (string, error)
	// Registered stores a client Tapas registered itself on c, which has
	// none, when c.Rev is still its revision.
	Registered(ctx context.Context, actor model.Actor, c connectors.Connector, clientID, secret string) (connectors.Connector, error)
	// Pin pins the authorization server s on c, which has none, when c.Rev
	// is still its revision (store.ErrConflict otherwise).
	Pin(ctx context.Context, actor model.Actor, c connectors.Connector, s connectors.AuthServer) (connectors.Connector, error)
	// ServerMoved records that c's MCP server names another authorization
	// server than the one pinned on c.
	ServerMoved(ctx context.Context, actor model.Actor, c connectors.Connector) error
}

// Config wires a Service. Every field but InternalHosts and Now is
// required.
type Config struct {
	DB *store.Store
	// Seal is the gateway's sessions key; it seals the PKCE verifier.
	Seal    tink.AEAD
	Clients Clients
	// HTTP reaches the MCP and authorization servers: the guarded client
	// (httpx.Guard), which follows no redirect.
	HTTP *http.Client
	// InternalHosts may be named over plain http (connectors.internalHosts);
	// every other discovered address must be https.
	InternalHosts []string
	// RedirectURI is the deployment's one redirect address
	// (connectors.RedirectURI of its public URL).
	RedirectURI string
	// Now is the clock; nil is the real one.
	Now func() time.Time
}

// Service runs sign-ins to connectors.
type Service struct {
	c     Config
	mu    sync.Mutex
	cache map[string]cachedServer
}

var _ vault.OAuth = (*Service)(nil)

// New returns the Service over c. It panics without a required field.
func New(c Config) *Service {
	if c.DB == nil || c.Seal == nil || c.Clients == nil || c.HTTP == nil {
		panic("oauth.New: Config.DB, Seal, Clients and HTTP are required")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Service{c: c, cache: map[string]cachedServer{}}
}

const stateTable = "connector_oauth_states"

func verifierAD(hash []byte) []byte {
	return connectors.SealContext(stateTable, hex.EncodeToString(hash), "verifier_sealed")
}

func hashState(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

// Start begins person's sign-in to c, for subject's connection (person, or
// "" for the company's), and returns the vendor's address to send them to.
// Who may do so is the caller's to check. The first sign-in pins c's
// authorization server; a later one is connectors.ErrServerMoved when the
// MCP server names another since. With no client on c, one is registered
// when the server allows it (actor is on the audit log for both), else it
// is connectors.ErrNeedsClient.
func (s *Service) Start(ctx context.Context, actor model.Actor, c connectors.Connector, person, subject string) (string, error) {
	if s.c.RedirectURI == "" {
		return "", errors.New("oauth: no redirect address: set the platform's public URL (mcp.publicURL)")
	}
	c, e, err := s.startEndpoints(ctx, actor, c)
	if err != nil {
		return "", err
	}
	if c, err = s.client(ctx, actor, c, e); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	verifier := oauth2.GenerateVerifier()
	hash := hashState(state)
	sealed, err := s.c.Seal.Encrypt([]byte(verifier), verifierAD(hash))
	if err != nil {
		return "", err
	}
	issuer := ""
	if e.issParam {
		issuer = e.issuer
	}
	now := s.c.Now()
	if err := s.c.DB.ConnectorConnections().StartOAuth(ctx, store.ConnectorOAuthState{
		StateHash: hash, ConnectorID: c.ID, ConnectorRev: c.Rev, Person: person, Subject: subject, VerifierSealed: sealed,
		RedirectURI: s.c.RedirectURI, Issuer: issuer, CreatedAt: now, ExpiresAt: now.Add(StateTTL),
	}); err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if e.resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", e.resource))
	}
	return s.config(c, e, "", s.c.RedirectURI).AuthCodeURL(state, opts...), nil
}

// client is c with an OAuth client: its own, or one registered now (RFC
// 7591) and stored on it.
func (s *Service) client(ctx context.Context, actor model.Actor, c connectors.Connector, e endpoints) (connectors.Connector, error) {
	if c.OAuth.ClientID != "" {
		return c, nil
	}
	if e.register == "" {
		return c, connectors.ErrNeedsClient
	}
	rctx, cancel := context.WithTimeout(ctx, HTTPTimeout)
	defer cancel()
	res, err := oauthex.RegisterClient(rctx, e.register, &oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{s.c.RedirectURI}, TokenEndpointAuthMethod: "client_secret_basic",
		GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		ClientName: "Tapas", Scope: strings.Join(c.Scopes, " "),
	}, s.c.HTTP)
	if err != nil {
		var re *oauthex.ClientRegistrationError
		if errors.As(err, &re) {
			return c, connectors.NewOAuthError(re.ErrorCode)
		}
		return c, &connectors.OAuthError{Code: connectors.CodeUnreachable}
	}
	got, err := s.c.Clients.Registered(ctx, actor, c, res.ClientID, res.ClientSecret)
	if errors.Is(err, store.ErrConflict) {
		// Someone else registered or changed it meanwhile: use theirs.
		if got, err = s.c.Clients.Get(ctx, c.ID); err == nil && got.OAuth.ClientID == "" {
			err = connectors.ErrNeedsClient
		}
	}
	return got, err
}

// Complete finishes, for person, the sign-in the vendor sent them back
// from with state, code and iss: the state must be one person started on
// this connector revision, unused and not expired (else
// connectors.ErrSignInExpired; the state is spent either way); when the
// server said it names itself, iss must be it. The code is exchanged with
// the PKCE verifier and the same redirect address.
func (s *Service) Complete(ctx context.Context, person, state, code, iss string) (vault.Grant, error) {
	st, err := s.c.DB.ConnectorConnections().TakeOAuthState(ctx, hashState(state))
	if errors.Is(err, store.ErrNotFound) {
		return vault.Grant{}, connectors.ErrSignInExpired
	}
	if err != nil {
		return vault.Grant{}, err
	}
	if st.Person != person || !s.c.Now().Before(st.ExpiresAt) {
		return vault.Grant{}, connectors.ErrSignInExpired
	}
	c, err := s.c.Clients.Get(ctx, st.ConnectorID)
	if errors.Is(err, store.ErrNotFound) || err == nil && c.Rev != st.ConnectorRev {
		return vault.Grant{}, connectors.ErrSignInExpired
	}
	if err != nil {
		return vault.Grant{}, err
	}
	if st.Issuer != "" && iss != st.Issuer {
		return vault.Grant{}, &connectors.OAuthError{Code: connectors.CodeIssuer}
	}
	verifier, err := s.c.Seal.Decrypt(st.VerifierSealed, verifierAD(st.StateHash))
	if err != nil {
		return vault.Grant{}, errors.New("oauth: the sign-in's verifier does not open with this gateway's sessions key")
	}
	e, err := pinnedEndpoints(c)
	if err != nil {
		return vault.Grant{}, err
	}
	secret, err := s.c.Clients.ClientSecret(ctx, c.ID)
	if err != nil {
		return vault.Grant{}, err
	}
	opts := []oauth2.AuthCodeOption{oauth2.VerifierOption(string(verifier))}
	if e.resource != "" {
		opts = append(opts, oauth2.SetAuthURLParam("resource", e.resource))
	}
	hctx, cancel := s.httpContext(ctx)
	defer cancel()
	tok, err := s.config(c, e, secret, st.RedirectURI).Exchange(hctx, code, opts...)
	if err != nil {
		return vault.Grant{}, tokenError(err)
	}
	t, err := tokensOf(tok, c.Scopes)
	if err != nil {
		return vault.Grant{}, err
	}
	return vault.Grant{Connector: c, Subject: st.Subject, Tokens: t}, nil
}

// Refresh trades refresh for new tokens at c's token endpoint. A refusal
// is a *connectors.OAuthError with the server's code (invalid_grant: for
// good).
func (s *Service) Refresh(ctx context.Context, c connectors.Connector, refresh string) (vault.Tokens, error) {
	e, err := pinnedEndpoints(c)
	if err != nil {
		return vault.Tokens{}, err
	}
	secret, err := s.c.Clients.ClientSecret(ctx, c.ID)
	if err != nil {
		return vault.Tokens{}, err
	}
	hctx, cancel := s.httpContext(ctx)
	defer cancel()
	tok, err := s.config(c, e, secret, s.c.RedirectURI).TokenSource(hctx, &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		return vault.Tokens{}, tokenError(err)
	}
	return tokensOf(tok, c.Scopes)
}

// config is the oauth2 client of c at e. A client without a secret sends
// its id in the body; one with a secret uses basic auth, unless the server
// only takes it in the body.
func (s *Service) config(c connectors.Connector, e endpoints, secret, redirectURI string) *oauth2.Config {
	style := oauth2.AuthStyleInHeader
	if secret == "" || len(e.authMethods) > 0 && !slices.Contains(e.authMethods, "client_secret_basic") && slices.Contains(e.authMethods, "client_secret_post") {
		style = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID: c.OAuth.ClientID, ClientSecret: secret, RedirectURL: redirectURI, Scopes: c.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: e.authorize, TokenURL: e.token, AuthStyle: style},
	}
}

// httpContext makes oauth2 use the configured client, under HTTPTimeout.
func (s *Service) httpContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, s.c.HTTP), HTTPTimeout)
}

// tokenError is err from a token endpoint reduced to its OAuth error code.
func tokenError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return connectors.NewOAuthError(re.ErrorCode)
	}
	return &connectors.OAuthError{Code: connectors.CodeUnreachable}
}

// tokensOf is what tok holds; requested are the scopes asked for, taken as
// granted when the server does not say.
func tokensOf(tok *oauth2.Token, requested []string) (vault.Tokens, error) {
	if tok.AccessToken == "" || tok.TokenType != "" && !strings.EqualFold(tok.TokenType, "bearer") {
		return vault.Tokens{}, &connectors.OAuthError{Code: "unsupported_token_type"}
	}
	t := vault.Tokens{Access: tok.AccessToken, Refresh: tok.RefreshToken, Expiry: tok.Expiry, Scopes: requested}
	if granted, ok := tok.Extra("scope").(string); ok && granted != "" {
		t.Scopes = strings.Fields(granted)
	}
	return t, nil
}
