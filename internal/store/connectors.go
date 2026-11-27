package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store/dbtime"
	"github.com/nebari-dev/javo/internal/store/sqlcgen"
)

// Connector is one row of the connectors table: an MCP server an admin
// added. OAuthClientSecretSealed is the OAuth client secret as the gateway
// sealed it for this row (nil: none); the clear value is never stored.
// ToolModes maps an upstream tool name to read, write or off; Tools is the
// last snapshot of the upstream tool list. OAuthIssuer and OAuthServer are
// the authorization server pinned at the first discovery ("" and zero
// before), OAuthServerMoved whether a later discovery named another
// issuer. Rev is new on every write.
type Connector struct {
	ID, Slug, Name, CatalogID, URL   string
	Enabled                          bool
	OAuthRegistration, OAuthClientID string
	OAuthClientSecretSealed          []byte
	OAuthAuthorizeURL, OAuthTokenURL string
	OAuthRevokeURL                   string
	OAuthIssuer                      string
	OAuthServer                      ConnectorAuthServer
	OAuthServerMoved                 bool
	Scopes, AllowedGroups            []string
	ToolModes                        map[string]string
	Tools                            []ConnectorTool
	Rev                              string
	CreatedAt, UpdatedAt             time.Time
}

// ConnectorTool is one upstream tool as last listed.
type ConnectorTool struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ReadOnlyHint bool   `json:"readOnlyHint,omitempty"`
}

// ConnectorAuthServer is what a pinned authorization server's metadata
// said: its endpoints, whether it names itself on the redirect (RFC 9207)
// and its token endpoint's client authentication methods.
type ConnectorAuthServer struct {
	AuthorizeURL     string   `json:"authorizeUrl,omitempty"`
	TokenURL         string   `json:"tokenUrl,omitempty"`
	RevokeURL        string   `json:"revokeUrl,omitempty"`
	RegistrationURL  string   `json:"registrationUrl,omitempty"`
	IssParameter     bool     `json:"issParameter,omitempty"`
	TokenAuthMethods []string `json:"tokenAuthMethods,omitempty"`
}

// Connectors is the connectors table.
type Connectors struct{ s *Store }

// Connectors returns the connectors table.
func (s *Store) Connectors() *Connectors { return &Connectors{s} }

const connectorKind = "connector"

// connectorAudit is the audit row of a write to c: its slug and id, and the
// names of the fields that changed (never a value).
func connectorAudit(action string, c Connector, changed []string) *auditRow {
	var detail any
	if len(changed) > 0 {
		detail = map[string][]string{"fields": changed}
	}
	return &auditRow{action: action, kind: connectorKind, name: c.Slug, id: c.ID, detail: detail}
}

// Create stores c as a new connector. seal gets the new row's id and returns
// the client secret sealed for it, or nil for none. A taken slug is
// ErrExists. It is audited.
func (x *Connectors) Create(ctx context.Context, actor model.Actor, c Connector, seal sealFunc) (Connector, error) {
	c.ID = newID()
	sealed, err := seal(c.ID)
	if err != nil {
		return Connector{}, err
	}
	c.OAuthClientSecretSealed = sealed
	c.Rev = newID()
	t := now()
	c.CreatedAt, c.UpdatedAt = t, t
	cols, err := connectorJSON(c)
	if err != nil {
		return Connector{}, err
	}
	var out Connector
	err = x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.InsertConnector(ctx, sqlcgen.InsertConnectorParams{
			ID: c.ID, Slug: c.Slug, Name: c.Name, CatalogID: c.CatalogID, Url: c.URL, Enabled: c.Enabled,
			OauthRegistration: c.OAuthRegistration, OauthClientID: c.OAuthClientID, OauthClientSecretSealed: c.OAuthClientSecretSealed,
			OauthAuthorizeUrl: c.OAuthAuthorizeURL, OauthTokenUrl: c.OAuthTokenURL, OauthRevokeUrl: c.OAuthRevokeURL,
			Scopes: cols.scopes, AllowedGroups: cols.groups, ToolModes: cols.modes, Tools: cols.tools,
			Rev: c.Rev, CreatedAt: dbtime.From(t), UpdatedAt: dbtime.From(t),
		})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrExists
		}
		out, err = readConnector(q.GetConnector(ctx, c.ID))
		return effect{audit: connectorAudit("connector.create", out, nil)}, err
	})
	return out, err
}

// Get returns the connector with id, or ErrNotFound.
func (x *Connectors) Get(ctx context.Context, id string) (Connector, error) {
	return readConnector(x.s.q.GetConnector(ctx, id))
}

// BySlug returns the connector called slug, or ErrNotFound.
func (x *Connectors) BySlug(ctx context.Context, slug string) (Connector, error) {
	return readConnector(x.s.q.GetConnectorBySlug(ctx, slug))
}

// List returns every connector by slug.
func (x *Connectors) List(ctx context.Context) ([]Connector, error) {
	return connectorList(x.s.q.ListConnectors(ctx))
}

// ListEnabled returns the enabled connectors by slug.
func (x *Connectors) ListEnabled(ctx context.Context) ([]Connector, error) {
	return connectorList(x.s.q.ListEnabledConnectors(ctx))
}

// Update saves c over the stored row when the stored rev is still c.Rev
// (ErrConflict otherwise: someone else saved first; ErrNotFound when there
// is no such row) and records changed, the names of the fields that changed,
// on the audit log. Slug, catalog id and tools are not changed here.
func (x *Connectors) Update(ctx context.Context, actor model.Actor, c Connector, changed []string) (Connector, error) {
	return x.update(ctx, actor, c, changed, false)
}

// Move is Update for a change that makes what people connected unusable
// (another address, authorization server or client): it also deletes
// every connection to the connector in the same transaction, so one
// stored while the change was being made (after the caller revoked the
// others) does not survive it. ConnectorConnections.Put waits for or fails
// against it, never lands after it.
func (x *Connectors) Move(ctx context.Context, actor model.Actor, c Connector, changed []string) (Connector, error) {
	return x.update(ctx, actor, c, changed, true)
}

func (x *Connectors) update(ctx context.Context, actor model.Actor, c Connector, changed []string, disconnect bool) (Connector, error) {
	cols, err := connectorJSON(c)
	if err != nil {
		return Connector{}, err
	}
	var out Connector
	err = x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.UpdateConnector(ctx, sqlcgen.UpdateConnectorParams{
			Name: c.Name, Url: c.URL, Enabled: c.Enabled,
			OauthRegistration: c.OAuthRegistration, OauthClientID: c.OAuthClientID, OauthClientSecretSealed: c.OAuthClientSecretSealed,
			OauthAuthorizeUrl: c.OAuthAuthorizeURL, OauthTokenUrl: c.OAuthTokenURL, OauthRevokeUrl: c.OAuthRevokeURL,
			Scopes: cols.scopes, AllowedGroups: cols.groups, ToolModes: cols.modes,
			OauthIssuer: c.OAuthIssuer, OauthServer: cols.server, OauthServerMoved: c.OAuthServerMoved,
			Rev: newID(), UpdatedAt: dbtime.From(now()), ID: c.ID, Rev_2: c.Rev,
		})
		if err != nil {
			return effect{}, err
		}
		out, err = readConnector(q.GetConnector(ctx, c.ID))
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrConflict
		}
		if disconnect {
			if _, err := q.DeleteConnectorConnectionsForConnector(ctx, c.ID); err != nil {
				return effect{}, err
			}
		}
		return effect{audit: connectorAudit("connector.update", out, changed)}, nil
	})
	return out, err
}

// PinAuthServer pins the authorization server issuer, as server describes
// it, on the connector with id when its rev is still rev and none is pinned
// (ErrConflict otherwise; ErrNotFound when there is no such row). It is
// audited.
func (x *Connectors) PinAuthServer(ctx context.Context, actor model.Actor, id, rev, issuer string, server ConnectorAuthServer) (Connector, error) {
	text, err := marshal(server, "{}")
	if err != nil {
		return Connector{}, err
	}
	var out Connector
	err = x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.PinConnectorAuthServer(ctx, sqlcgen.PinConnectorAuthServerParams{
			OauthIssuer: issuer, OauthServer: text, Rev: newID(), UpdatedAt: dbtime.From(now()), ID: id, Rev_2: rev,
		})
		if err != nil {
			return effect{}, err
		}
		if out, err = readConnector(q.GetConnector(ctx, id)); err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrConflict
		}
		return effect{audit: connectorAudit("connector.update", out, []string{"oauth.issuer"})}, nil
	})
	return out, err
}

// AuthServerMoved records that the metadata of the connector with id now
// names another issuer than issuer, the one pinned; nothing is done when
// another is pinned now or it is already recorded. It is audited.
func (x *Connectors) AuthServerMoved(ctx context.Context, actor model.Actor, id, issuer string) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.MarkConnectorAuthServerMoved(ctx, sqlcgen.MarkConnectorAuthServerMovedParams{
			Rev: newID(), UpdatedAt: dbtime.From(now()), ID: id, OauthIssuer: issuer,
		})
		if err != nil || n == 0 {
			return effect{}, err
		}
		c, err := readConnector(q.GetConnector(ctx, id))
		return effect{audit: connectorAudit("connector.update", c, []string{"oauth.serverMoved"})}, err
	})
}

// SetTools replaces the snapshot of the upstream tool list of the connector
// with id under a new rev, and records it on the audit log (the field name
// only).
func (x *Connectors) SetTools(ctx context.Context, actor model.Actor, id string, tools []ConnectorTool) (Connector, error) {
	text, err := marshal(nonNil(tools), "[]")
	if err != nil {
		return Connector{}, err
	}
	var out Connector
	err = x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		n, err := q.SetConnectorTools(ctx, sqlcgen.SetConnectorToolsParams{Tools: text, Rev: newID(), UpdatedAt: dbtime.From(now()), ID: id})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrNotFound
		}
		out, err = readConnector(q.GetConnector(ctx, id))
		return effect{audit: connectorAudit("connector.update", out, []string{"tools"})}, err
	})
	return out, err
}

// Delete removes the connector with id (ErrNotFound when there is none)
// and records it on the audit log.
func (x *Connectors) Delete(ctx context.Context, actor model.Actor, id string) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		c, err := readConnector(q.GetConnector(ctx, id))
		if err != nil {
			return effect{}, err
		}
		if _, err := q.DeleteConnector(ctx, id); err != nil {
			return effect{}, err
		}
		return effect{audit: connectorAudit("connector.delete", c, nil)}, nil
	})
}

// connectorCols is the JSON text of a connector's list and map columns.
type connectorCols struct{ scopes, groups, modes, tools, server string }

func connectorJSON(c Connector) (connectorCols, error) {
	var out connectorCols
	var err error
	if out.scopes, err = marshal(nonNil(c.Scopes), "[]"); err != nil {
		return out, err
	}
	if out.groups, err = marshal(nonNil(c.AllowedGroups), "[]"); err != nil {
		return out, err
	}
	modes := c.ToolModes
	if modes == nil {
		modes = map[string]string{}
	}
	if out.modes, err = marshal(modes, "{}"); err != nil {
		return out, err
	}
	if out.tools, err = marshal(nonNil(c.Tools), "[]"); err != nil {
		return out, err
	}
	out.server, err = marshal(c.OAuthServer, "{}")
	return out, err
}

// nonNil is s, or an empty slice for nil, so it is stored as [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func readConnector(r sqlcgen.Connector, err error) (Connector, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return Connector{}, ErrNotFound
	}
	if err != nil {
		return Connector{}, err
	}
	c := Connector{
		ID: r.ID, Slug: r.Slug, Name: r.Name, CatalogID: r.CatalogID, URL: r.Url, Enabled: r.Enabled,
		OAuthRegistration: r.OauthRegistration, OAuthClientID: r.OauthClientID, OAuthClientSecretSealed: r.OauthClientSecretSealed,
		OAuthAuthorizeURL: r.OauthAuthorizeUrl, OAuthTokenURL: r.OauthTokenUrl, OAuthRevokeURL: r.OauthRevokeUrl,
		OAuthIssuer: r.OauthIssuer, OAuthServerMoved: r.OauthServerMoved,
		Rev: r.Rev, CreatedAt: r.CreatedAt.Time(), UpdatedAt: r.UpdatedAt.Time(),
		Scopes: []string{}, AllowedGroups: []string{}, ToolModes: map[string]string{}, Tools: []ConnectorTool{},
	}
	for _, col := range []struct {
		text string
		into any
	}{{r.Scopes, &c.Scopes}, {r.AllowedGroups, &c.AllowedGroups}, {r.ToolModes, &c.ToolModes}, {r.Tools, &c.Tools}, {r.OauthServer, &c.OAuthServer}} {
		if err := unmarshal(col.text, col.into); err != nil {
			return Connector{}, err
		}
	}
	return c, nil
}

func connectorList(rows []sqlcgen.Connector, err error) ([]Connector, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Connector, 0, len(rows))
	for _, r := range rows {
		c, err := readConnector(r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
