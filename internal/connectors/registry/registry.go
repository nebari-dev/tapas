// Package registry is the connectors an admin added: create, read, change
// and delete them over the store's connectors table. It seals each
// connector's OAuth client secret with the gateway's sessions key (the
// one key all replicas share, as package aisettings does for the admin's
// AI key), bound to the row and column it is stored in; the secret is never
// returned in a view, only opened for the OAuth client that uses it.
package registry

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/catalog"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// Config is what a Registry needs; every field but InternalHosts is
// required.
type Config struct {
	DB *store.Store
	// Seal is the gateway's sessions key. It must survive restarts: a
	// secret sealed with a key made at start no longer opens after one.
	Seal tink.AEAD
	// InternalHosts are the hosts a connector may be reached at over plain
	// http (connectors.internalHosts in the platform config).
	InternalHosts []string
	// Connections disconnects everyone from a connector (the token vault);
	// nil when nobody can connect.
	Connections Connections
}

// Connections disconnects everyone from a connector: it revokes and deletes
// their tokens.
type Connections interface {
	DeleteForConnector(ctx context.Context, actor model.Actor, c connectors.Connector) error
}

// Registry is the connectors an admin added.
type Registry struct{ c Config }

// New returns the registry over c. It panics without c.DB or c.Seal.
func New(c Config) *Registry {
	if c.DB == nil || c.Seal == nil {
		panic("registry.New: Config.DB and Config.Seal are required")
	}
	return &Registry{c}
}

// Input is what an admin saves. ClientSecret is write-only: empty keeps the
// stored one, which works only while the address, the OAuth endpoints and
// the client id stay the same. CatalogID and Slug are fixed once created;
// Update ignores them. Rev is the revision the change was made on (Update
// only).
type Input struct {
	CatalogID, Slug, Name, URL        string
	AuthMode                          connectors.AuthMode
	Enabled                           bool
	ClientID, ClientSecret            string
	AuthorizeURL, TokenURL, RevokeURL string
	Scopes, AllowedGroups             []string
	ToolModes                         map[string]connectors.ToolMode
	Rev                               string
}

const (
	table        = "connectors"
	secretColumn = "oauth_client_secret_sealed"
)

func secretAD(id string) []byte { return connectors.SealContext(table, id, secretColumn) }

// Create validates in and stores it as a new connector. With a CatalogID,
// what in leaves empty is taken from that catalogue entry. A refused field
// is a *connectors.FieldError, a taken slug store.ErrExists.
func (r *Registry) Create(ctx context.Context, actor model.Actor, in Input) (connectors.Connector, error) {
	if in.CatalogID != "" {
		e, ok := catalog.Get(in.CatalogID)
		if !ok {
			return connectors.Connector{}, &connectors.FieldError{Field: "catalogId", Code: reasons.MsgConnectorCatalogUnknown}
		}
		in = fill(in, e)
	}
	next := connectorOf(in)
	if err := next.Validate(r.c.InternalHosts); err != nil {
		return connectors.Connector{}, err
	}
	if in.ClientSecret != "" && in.ClientID == "" {
		return connectors.Connector{}, needsClient()
	}
	row := rowOf(next, store.Connector{})
	if in.ClientID != "" {
		row.OAuthRegistration = connectors.RegistrationStatic
	}
	created, err := r.c.DB.Connectors().Create(ctx, actor, row, func(id string) ([]byte, error) {
		if in.ClientSecret == "" {
			return nil, nil
		}
		return r.c.Seal.Encrypt([]byte(in.ClientSecret), secretAD(id))
	})
	if err != nil {
		return connectors.Connector{}, err
	}
	return viewOf(created), nil
}

// fill is in with what it leaves empty taken from e.
func fill(in Input, e catalog.Entry) Input {
	c := e.Connector()
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&in.Slug, c.Slug}, {&in.Name, c.Name}, {&in.URL, c.URL},
	} {
		if *f.dst == "" {
			*f.dst = f.src
		}
	}
	if in.AuthMode == "" {
		in.AuthMode = c.AuthMode
	}
	if in.AuthorizeURL == "" && in.TokenURL == "" && in.RevokeURL == "" {
		in.AuthorizeURL, in.TokenURL, in.RevokeURL = c.OAuth.AuthorizeURL, c.OAuth.TokenURL, c.OAuth.RevokeURL
	}
	if in.Scopes == nil {
		in.Scopes = c.Scopes
	}
	if in.ToolModes == nil {
		in.ToolModes = c.ToolModes
	}
	return in
}

// Get returns the connector with id, or store.ErrNotFound.
func (r *Registry) Get(ctx context.Context, id string) (connectors.Connector, error) {
	return view(r.c.DB.Connectors().Get(ctx, id))
}

// BySlug returns the connector called slug, or store.ErrNotFound.
func (r *Registry) BySlug(ctx context.Context, slug string) (connectors.Connector, error) {
	return view(r.c.DB.Connectors().BySlug(ctx, slug))
}

// List returns every connector by slug.
func (r *Registry) List(ctx context.Context) ([]connectors.Connector, error) {
	return views(r.c.DB.Connectors().List(ctx))
}

// ListEnabled returns the enabled connectors by slug.
func (r *Registry) ListEnabled(ctx context.Context) ([]connectors.Connector, error) {
	return views(r.c.DB.Connectors().ListEnabled(ctx))
}

// Update validates in and saves it over the connector with id, when in.Rev
// is still its revision (store.ErrConflict otherwise; store.ErrNotFound
// when there is none). The names of the changed fields go on the audit log.
// A new address, new OAuth endpoints or a new client id need the client
// secret again when one is stored, and drop what people connected; a new
// auth mode drops what was connected too.
func (r *Registry) Update(ctx context.Context, actor model.Actor, id string, in Input) (connectors.Connector, error) {
	prev, err := r.c.DB.Connectors().Get(ctx, id)
	if err != nil {
		return connectors.Connector{}, err
	}
	if prev.Rev != in.Rev {
		return connectors.Connector{}, store.ErrConflict
	}
	in.Slug, in.CatalogID = prev.Slug, prev.CatalogID
	// Any change of the address is a move, even of its path or case: the
	// vault sends a token only to the exact address it was issued for, and
	// another path may name another authorization server. So is saving a
	// connector whose MCP server now names another authorization server
	// than the pinned one: the save is the admin trusting it.
	moved := prev.URL != in.URL || prev.OAuthAuthorizeURL != in.AuthorizeURL ||
		prev.OAuthTokenURL != in.TokenURL || prev.OAuthRevokeURL != in.RevokeURL || prev.OAuthServerMoved
	if moved && prev.OAuthRegistration == connectors.RegistrationDynamic && in.ClientID == prev.OAuthClientID && in.ClientSecret == "" {
		// A client Tapas registered belongs to the old server: drop it, so
		// one is registered where sign-ins go now.
		in.ClientID = ""
	}
	next := connectorOf(in)
	if err := next.Validate(r.c.InternalHosts); err != nil {
		return connectors.Connector{}, err
	}
	row := rowOf(next, prev)
	if moved {
		// The next sign-in discovers and pins the server again.
		row.OAuthIssuer, row.OAuthServer, row.OAuthServerMoved = "", store.ConnectorAuthServer{}, false
	}
	newClient := prev.OAuthClientID != in.ClientID
	secretChanged := false
	switch {
	case in.ClientSecret != "":
		if in.ClientID == "" {
			return connectors.Connector{}, needsClient()
		}
		sealed, err := r.c.Seal.Encrypt([]byte(in.ClientSecret), secretAD(id))
		if err != nil {
			return connectors.Connector{}, fmt.Errorf("seal the client secret: %w", err)
		}
		row.OAuthClientSecretSealed, row.OAuthRegistration, secretChanged = sealed, connectors.RegistrationStatic, true
	case in.ClientID == "":
		row.OAuthClientSecretSealed, row.OAuthRegistration, secretChanged = nil, "", prev.OAuthClientSecretSealed != nil
	case prev.OAuthClientSecretSealed != nil && (moved || newClient):
		return connectors.Connector{}, &connectors.FieldError{Field: "oauth.clientSecret", Code: reasons.MsgConnectorSecretForNewAddress}
	case newClient:
		row.OAuthRegistration = connectors.RegistrationStatic
	}
	// A stored token must never be sent to a host other than the one it
	// was issued for, nor used under another client. They are revoked where
	// they were issued, with the client they were issued to. Switching whose
	// account is used drops them too, so switching back does not silently
	// revive a connection nobody made again.
	forget := moved || newClient || prev.AuthMode != string(in.AuthMode)
	if forget {
		if err := r.forgetConnections(ctx, actor, viewOf(prev)); err != nil {
			return connectors.Connector{}, err
		}
	}
	save := r.c.DB.Connectors().Update
	if forget {
		// Move also drops a connection stored since the ones above were
		// revoked (a sign-in that finished meanwhile).
		save = r.c.DB.Connectors().Move
	}
	saved, err := save(ctx, actor, row, changedFields(prev, row, secretChanged))
	if err != nil {
		return connectors.Connector{}, err
	}
	return viewOf(saved), nil
}

// Delete removes the connector with id, after what people connected to it
// (store.ErrNotFound when there is none).
func (r *Registry) Delete(ctx context.Context, actor model.Actor, id string) error {
	prev, err := r.c.DB.Connectors().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := r.forgetConnections(ctx, actor, viewOf(prev)); err != nil {
		return err
	}
	return r.c.DB.Connectors().Delete(ctx, actor, id)
}

// forgetConnections revokes and deletes every account connected to c (as
// it is before the change): on delete, and when its address, OAuth
// endpoints, client or auth mode change (people then connect again).
func (r *Registry) forgetConnections(ctx context.Context, actor model.Actor, c connectors.Connector) error {
	if r.c.Connections == nil {
		return nil
	}
	return r.c.Connections.DeleteForConnector(ctx, actor, c)
}

// Registered stores the OAuth client Tapas registered itself (RFC 7591) on
// c, which had none, when c.Rev is still its revision (store.ErrConflict
// otherwise). secret is "" for a client without one.
func (r *Registry) Registered(ctx context.Context, actor model.Actor, c connectors.Connector, clientID, secret string) (connectors.Connector, error) {
	prev, err := r.c.DB.Connectors().Get(ctx, c.ID)
	if err != nil {
		return connectors.Connector{}, err
	}
	if prev.Rev != c.Rev || prev.OAuthClientID != "" {
		return connectors.Connector{}, store.ErrConflict
	}
	row := prev
	row.OAuthClientID, row.OAuthRegistration, row.OAuthClientSecretSealed = clientID, connectors.RegistrationDynamic, nil
	if secret != "" {
		if row.OAuthClientSecretSealed, err = r.c.Seal.Encrypt([]byte(secret), secretAD(c.ID)); err != nil {
			return connectors.Connector{}, fmt.Errorf("seal the client secret: %w", err)
		}
	}
	return view(r.c.DB.Connectors().Update(ctx, actor, row, changedFields(prev, row, secret != "")))
}

// Pin pins the authorization server s on c, which has none, when c.Rev is
// still its revision (store.ErrConflict otherwise), and returns c as saved.
func (r *Registry) Pin(ctx context.Context, actor model.Actor, c connectors.Connector, s connectors.AuthServer) (connectors.Connector, error) {
	return view(r.c.DB.Connectors().PinAuthServer(ctx, actor, c.ID, c.Rev, s.Issuer, store.ConnectorAuthServer{
		AuthorizeURL: s.AuthorizeURL, TokenURL: s.TokenURL, RevokeURL: s.RevokeURL, RegistrationURL: s.RegistrationURL,
		IssParameter: s.IssParameter, TokenAuthMethods: s.TokenAuthMethods,
	}))
}

// ServerMoved records that c's MCP server now names another authorization
// server than the one pinned on c: no sign-in starts until an admin saves c
// again.
func (r *Registry) ServerMoved(ctx context.Context, actor model.Actor, c connectors.Connector) error {
	if c.OAuth.Server == nil {
		return nil
	}
	return r.c.DB.Connectors().AuthServerMoved(ctx, actor, c.ID, c.OAuth.Server.Issuer)
}

// SetTools replaces the snapshot of the connector's upstream tool list.
func (r *Registry) SetTools(ctx context.Context, actor model.Actor, id string, tools []connectors.Tool) (connectors.Connector, error) {
	rows := make([]store.ConnectorTool, 0, len(tools))
	for _, t := range tools {
		rows = append(rows, store.ConnectorTool{Name: t.Name, Description: t.Description, ReadOnlyHint: t.ReadOnlyHint})
	}
	return view(r.c.DB.Connectors().SetTools(ctx, actor, id, rows))
}

// ClientSecret opens the stored OAuth client secret of the connector with
// id: "" when none is stored. It is for the OAuth client alone; never log
// or return it.
func (r *Registry) ClientSecret(ctx context.Context, id string) (string, error) {
	row, err := r.c.DB.Connectors().Get(ctx, id)
	if err != nil {
		return "", err
	}
	if row.OAuthClientSecretSealed == nil {
		return "", nil
	}
	b, err := r.c.Seal.Decrypt(row.OAuthClientSecretSealed, secretAD(id))
	if err != nil {
		return "", errors.New("the connector's client secret does not open with this gateway's sessions key; paste it again")
	}
	return string(b), nil
}

func needsClient() error {
	return &connectors.FieldError{Field: "oauth.clientId", Code: reasons.MsgConnectorNeedsOAuthClient}
}

// changedFields names, as the admin API does, what differs between prev
// and next; secretChanged says whether the secret was replaced or removed.
func changedFields(prev, next store.Connector, secretChanged bool) []string {
	var out []string
	add := func(name string, differs bool) {
		if differs {
			out = append(out, name)
		}
	}
	add("name", prev.Name != next.Name)
	add("url", prev.URL != next.URL)
	add("authMode", prev.AuthMode != next.AuthMode)
	add("enabled", prev.Enabled != next.Enabled)
	add("oauth.clientId", prev.OAuthClientID != next.OAuthClientID)
	add("oauth.clientSecret", secretChanged)
	add("oauth.authorizeUrl", prev.OAuthAuthorizeURL != next.OAuthAuthorizeURL)
	add("oauth.tokenUrl", prev.OAuthTokenURL != next.OAuthTokenURL)
	add("oauth.revokeUrl", prev.OAuthRevokeURL != next.OAuthRevokeURL)
	add("oauth.issuer", prev.OAuthIssuer != next.OAuthIssuer)
	add("scopes", !slices.Equal(prev.Scopes, next.Scopes))
	add("allowedGroups", !slices.Equal(prev.AllowedGroups, next.AllowedGroups))
	add("toolModes", !maps.Equal(prev.ToolModes, next.ToolModes))
	return out
}

// connectorOf is the connector in describes, for validation.
func connectorOf(in Input) connectors.Connector {
	return connectors.Connector{
		Slug: in.Slug, Name: in.Name, CatalogID: in.CatalogID, URL: in.URL, AuthMode: in.AuthMode, Enabled: in.Enabled,
		OAuth:  connectors.OAuth{ClientID: in.ClientID, AuthorizeURL: in.AuthorizeURL, TokenURL: in.TokenURL, RevokeURL: in.RevokeURL},
		Scopes: in.Scopes, AllowedGroups: in.AllowedGroups, ToolModes: in.ToolModes,
	}
}

// rowOf is prev with the fields an admin sets taken from c. The secret,
// registration, tools and rev stay prev's.
func rowOf(c connectors.Connector, prev store.Connector) store.Connector {
	row := prev
	row.Slug, row.Name, row.CatalogID, row.URL, row.AuthMode, row.Enabled = c.Slug, c.Name, c.CatalogID, c.URL, string(c.AuthMode), c.Enabled
	row.OAuthClientID, row.OAuthAuthorizeURL, row.OAuthTokenURL, row.OAuthRevokeURL = c.OAuth.ClientID, c.OAuth.AuthorizeURL, c.OAuth.TokenURL, c.OAuth.RevokeURL
	row.Scopes, row.AllowedGroups = slices.Clone(c.Scopes), slices.Clone(c.AllowedGroups)
	row.ToolModes = make(map[string]string, len(c.ToolModes))
	for k, v := range c.ToolModes {
		row.ToolModes[k] = string(v)
	}
	return row
}

// viewOf is row without its secret.
func viewOf(row store.Connector) connectors.Connector {
	c := connectors.Connector{
		ID: row.ID, Slug: row.Slug, Name: row.Name, CatalogID: row.CatalogID, URL: row.URL,
		AuthMode: connectors.AuthMode(row.AuthMode), Enabled: row.Enabled,
		OAuth: connectors.OAuth{
			Registration: row.OAuthRegistration, ClientID: row.OAuthClientID, SecretStored: row.OAuthClientSecretSealed != nil,
			AuthorizeURL: row.OAuthAuthorizeURL, TokenURL: row.OAuthTokenURL, RevokeURL: row.OAuthRevokeURL,
		},
		Scopes: row.Scopes, AllowedGroups: row.AllowedGroups, ToolModes: make(map[string]connectors.ToolMode, len(row.ToolModes)),
		Tools: make([]connectors.Tool, 0, len(row.Tools)), Rev: row.Rev, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.OAuthIssuer != "" {
		p := row.OAuthServer
		c.OAuth.Server = &connectors.AuthServer{
			Issuer: row.OAuthIssuer, AuthorizeURL: p.AuthorizeURL, TokenURL: p.TokenURL, RevokeURL: p.RevokeURL,
			RegistrationURL: p.RegistrationURL, IssParameter: p.IssParameter, TokenAuthMethods: p.TokenAuthMethods,
			Moved: row.OAuthServerMoved,
		}
	}
	for k, v := range row.ToolModes {
		c.ToolModes[k] = connectors.ToolMode(v)
	}
	for _, t := range row.Tools {
		c.Tools = append(c.Tools, connectors.Tool{Name: t.Name, Description: t.Description, ReadOnlyHint: t.ReadOnlyHint})
	}
	return c
}

func view(row store.Connector, err error) (connectors.Connector, error) {
	if err != nil {
		return connectors.Connector{}, err
	}
	return viewOf(row), nil
}

func views(rows []store.Connector, err error) ([]connectors.Connector, error) {
	if err != nil {
		return nil, err
	}
	out := make([]connectors.Connector, 0, len(rows))
	for _, row := range rows {
		out = append(out, viewOf(row))
	}
	return out, nil
}
