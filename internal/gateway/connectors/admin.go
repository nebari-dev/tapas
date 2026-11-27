// Package connectors is the HTTP API of connectors: the platform admins'
// routes that add, change and remove them, and the routes people connect
// their own accounts with: every connection is one person's own. The routes are
// registered in every build; without connectors (always so in a binary
// built with the tag local) they answer 409.
package connectors

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/catalog"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// API serves the connector routes.
type API struct {
	// Registry is the connectors; nil while connectors are off
	// (connectors.enabled), when every route answers 409.
	Registry *registry.Registry
	// Vault is the connected accounts; nil while connectors are off.
	Vault *vault.Vault
	// SignIn signs people in at vendors and Tools lists a connector's tools
	// upstream; nil while connectors are off and in a build without them.
	SignIn SignIn
	Tools  ToolLister
	Roles  config.Roles
	// PublicURL is mcp.publicURL, the OAuth redirect address's base.
	PublicURL string
}

// OAuthInput is a connector's OAuth client and endpoints as an admin sets them.
type OAuthInput struct {
	ClientID     string `json:"clientId,omitempty" maxLength:"512" doc:"The OAuth client id from the vendor; empty: Tapas registers a client itself when the vendor allows it"`
	ClientSecret string `json:"clientSecret,omitempty" maxLength:"4096" doc:"Write-only: replaces the stored secret; leave out to keep it, which only works while the address origin, the endpoints and the client id stay the same"`
	AuthorizeURL string `json:"authorizeUrl,omitempty" maxLength:"2048" doc:"Static authorize endpoint; empty: discovered from the MCP server"`
	TokenURL     string `json:"tokenUrl,omitempty" maxLength:"2048" doc:"Static token endpoint, set together with authorizeUrl"`
	RevokeURL    string `json:"revokeUrl,omitempty" maxLength:"2048" doc:"Static revocation endpoint (RFC 7009), optional"`
}

// ConnectorInput is what an admin saves.
type ConnectorInput struct {
	CatalogID     string            `json:"catalogId,omitempty" maxLength:"64" doc:"The catalogue entry this starts from; what is left out is taken from it. Fixed once created"`
	Slug          string            `json:"slug,omitempty" maxLength:"31" doc:"Short name, the prefix of its tools (slug__tool): 2 to 31 lowercase letters and digits. Fixed once created"`
	Name          string            `json:"name,omitempty" maxLength:"100"`
	URL           string            `json:"url,omitempty" maxLength:"2048" doc:"The MCP server's https address"`
	Enabled       bool              `json:"enabled,omitempty"`
	OAuth         OAuthInput        `json:"oauth,omitempty"`
	Scopes        []string          `json:"scopes,omitempty" maxItems:"50" doc:"OAuth scopes asked for"`
	AllowedGroups []string          `json:"allowedGroups,omitempty" maxItems:"100" doc:"Groups whose members may use it; * is everyone; empty is nobody"`
	ToolModes     map[string]string `json:"toolModes,omitempty" doc:"Tool name to read, write or off; a tool not listed is a write"`
	Rev           string            `json:"rev,omitempty" maxLength:"64" doc:"Required on PUT: the rev the change was made on"`
}

// AdminOAuth is a connector's OAuth client as an admin sees it: never its secret.
type AdminOAuth struct {
	ClientID     string `json:"clientId"`
	SecretStored bool   `json:"secretStored" doc:"A client secret is stored"`
	Registration string `json:"registration" enum:"static,dynamic," doc:"static: pasted; dynamic: registered by Tapas (RFC 7591); empty: no client yet"`
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	RevokeURL    string `json:"revokeUrl,omitempty"`
	Issuer       string `json:"issuer,omitempty" doc:"The authorization server sign-ins, refreshes and revocations go to, pinned at the first sign-in (discovered endpoints only)"`
	ServerMoved  bool   `json:"serverMoved,omitempty" doc:"The MCP server now names another authorization server: nobody can connect until an admin saves the connector again (with the client secret), which disconnects everyone"`
}

// ToolView is one tool the connector's MCP server listed.
type ToolView struct {
	Name         string `json:"name"`
	Description  string `json:"description,omitempty"`
	ReadOnlyHint bool   `json:"readOnlyHint" doc:"What the server claimed; only a suggestion for its mode"`
}

// AdminConnector is a connector as an admin sees it.
type AdminConnector struct {
	ID            string            `json:"id"`
	Rev           string            `json:"rev"`
	CatalogID     string            `json:"catalogId"`
	Slug          string            `json:"slug"`
	Name          string            `json:"name"`
	URL           string            `json:"url"`
	Enabled       bool              `json:"enabled"`
	OAuth         AdminOAuth        `json:"oauth"`
	Scopes        []string          `json:"scopes"`
	AllowedGroups []string          `json:"allowedGroups"`
	ToolModes     map[string]string `json:"toolModes"`
	Tools         []ToolView        `json:"tools" doc:"The last snapshot of the server's tool list"`
	RedirectURI   string            `json:"redirectUri,omitempty" doc:"This connector's own OAuth redirect address: paste it at the vendor"`
	Connections   int               `json:"connections" doc:"How many accounts are connected"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// CatalogEntry is one entry of the built-in catalogue.
type CatalogEntry struct {
	ID             string            `json:"id"`
	Slug           string            `json:"slug"`
	Name           string            `json:"name"`
	URL            string            `json:"url" doc:"The vendor's hosted MCP server; empty: the admin fills it in"`
	Scopes         []string          `json:"scopes"`
	ToolModes      map[string]string `json:"toolModes"`
	OAuthEndpoints string            `json:"oauthEndpoints" enum:"discovered,static"`
	OAuth          catalog.Endpoints `json:"oauth"`
	Setup          string            `json:"setup" doc:"What to do at the vendor"`
	Docs           string            `json:"docs" doc:"The vendor's documentation of its MCP server"`
}

type catalogOutput struct {
	Body struct {
		Items             []CatalogEntry `json:"items"`
		RedirectURIPrefix string         `json:"redirectUriPrefix,omitempty" doc:"A connector's OAuth redirect address is this followed by its slug; paste that at the vendor"`
	}
}

type listOutput struct {
	Body struct {
		Items []AdminConnector `json:"items"`
	}
}

type connectorOutput struct{ Body AdminConnector }

type createOutput struct {
	Status int
	Body   AdminConnector
}

type idInput struct {
	ID string `path:"id" maxLength:"64"`
}

type createInput struct{ Body ConnectorInput }

type updateInput struct {
	ID   string `path:"id" maxLength:"64"`
	Body ConnectorInput
}

// Register adds the admin routes to api.
func (a *API) Register(api huma.API) {
	const base = "/api/admin/connectors"
	conflict := []int{http.StatusConflict}
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminConnectorCatalog", Method: http.MethodGet, Path: base + "/catalog",
		Summary: "The built-in catalogue of connectors (platform admins)", Errors: conflict,
	}, func(context.Context, *struct{}) (*catalogOutput, error) {
		if a.Registry == nil {
			return nil, off()
		}
		out := &catalogOutput{}
		out.Body.Items, out.Body.RedirectURIPrefix = []CatalogEntry{}, connectors.RedirectURI(a.PublicURL, "")
		for _, e := range catalog.All() {
			out.Body.Items = append(out.Body.Items, CatalogEntry{
				ID: e.ID, Slug: e.Slug, Name: e.Name, URL: e.URL, Scopes: nonNil(e.Scopes),
				ToolModes: modes(e.ToolModes), OAuthEndpoints: e.OAuthEndpoints, OAuth: e.OAuth, Setup: e.Setup, Docs: e.Docs,
			})
		}
		return out, nil
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminListConnectors", Method: http.MethodGet, Path: base,
		Summary: "Every connector (platform admins)", Errors: conflict,
	}, func(ctx context.Context, _ *struct{}) (*listOutput, error) {
		if a.Registry == nil {
			return nil, off()
		}
		all, err := a.Registry.List(ctx)
		if err != nil {
			return nil, problem.Internal("list connectors", err)
		}
		out := &listOutput{}
		out.Body.Items = make([]AdminConnector, 0, len(all))
		for _, c := range all {
			v, err := a.adminView(ctx, c)
			if err != nil {
				return nil, err
			}
			out.Body.Items = append(out.Body.Items, v)
		}
		return out, nil
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminCreateConnector", Method: http.MethodPost, Path: base,
		Summary:       "Add a connector (platform admins)",
		Description:   "With catalogId, what the body leaves out is taken from that catalogue entry. The client secret is stored encrypted and never returned. Each change is on the audit log with the names of the fields, never their values.",
		DefaultStatus: http.StatusCreated, Errors: []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *createInput) (*createOutput, error) {
		if a.Registry == nil {
			return nil, off()
		}
		c, err := a.Registry.Create(ctx, authn.Actor(ctx), registryInput(in.Body))
		if err != nil {
			return nil, failed("add the connector", err, in.Body.Slug)
		}
		v, err := a.adminView(ctx, c)
		if err != nil {
			return nil, err
		}
		return &createOutput{Status: http.StatusCreated, Body: v}, nil
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminGetConnector", Method: http.MethodGet, Path: base + "/{id}",
		Summary: "One connector (platform admins)", Errors: []int{http.StatusNotFound, http.StatusConflict},
	}, func(ctx context.Context, in *idInput) (*connectorOutput, error) {
		if a.Registry == nil {
			return nil, off()
		}
		c, err := a.Registry.Get(ctx, in.ID)
		if err != nil {
			return nil, failed("read the connector", err, "")
		}
		v, err := a.adminView(ctx, c)
		if err != nil {
			return nil, err
		}
		return &connectorOutput{Body: v}, nil
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminUpdateConnector", Method: http.MethodPut, Path: base + "/{id}",
		Summary:     "Change a connector (platform admins)",
		Description: "rev must be the connector's current rev (409 otherwise). A new address (any change), new OAuth endpoints, a new client id, or saving a connector whose authorization server moved (oauth.serverMoved) need the client secret again when one is stored, and disconnect everyone who connected; the next sign-in pins the authorization server again.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *updateInput) (*connectorOutput, error) {
		if a.Registry == nil {
			return nil, off()
		}
		c, err := a.Registry.Update(ctx, authn.Actor(ctx), in.ID, registryInput(in.Body))
		if err != nil {
			return nil, failed("change the connector", err, "")
		}
		v, err := a.adminView(ctx, c)
		if err != nil {
			return nil, err
		}
		return &connectorOutput{Body: v}, nil
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminDeleteConnector", Method: http.MethodDelete, Path: base + "/{id}",
		Summary:       "Remove a connector and everyone's connection to it (platform admins)",
		DefaultStatus: http.StatusNoContent, Errors: []int{http.StatusNotFound, http.StatusConflict},
	}, func(ctx context.Context, in *idInput) (*struct{}, error) {
		if a.Registry == nil {
			return nil, off()
		}
		if err := a.Registry.Delete(ctx, authn.Actor(ctx), in.ID); err != nil {
			return nil, failed("remove the connector", err, "")
		}
		return nil, nil
	})

	a.registerRefresh(api, base)
	a.registerMember(api)
}

func off() error { return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgConnectorsOff)) }

// failed is the problem answer for err from the registry; slug names a
// taken slug.
func failed(what string, err error, slug string) error {
	var fe *connectors.FieldError
	switch {
	case errors.As(err, &fe):
		return problem.FieldError(fe.Field, fe.Code, fe.Args...)
	case errors.Is(err, store.ErrNotFound):
		return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgConnectorNotFound))
	case errors.Is(err, store.ErrExists):
		return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgConnectorSlugTaken, slug))
	case errors.Is(err, store.ErrConflict):
		return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgConnectorChanged))
	}
	return problem.Internal(what, err)
}

func registryInput(b ConnectorInput) registry.Input {
	in := registry.Input{
		CatalogID: b.CatalogID, Slug: b.Slug, Name: b.Name, URL: b.URL, Enabled: b.Enabled,
		ClientID: b.OAuth.ClientID, ClientSecret: b.OAuth.ClientSecret,
		AuthorizeURL: b.OAuth.AuthorizeURL, TokenURL: b.OAuth.TokenURL, RevokeURL: b.OAuth.RevokeURL,
		Scopes: b.Scopes, AllowedGroups: b.AllowedGroups, Rev: b.Rev,
	}
	if b.ToolModes != nil {
		in.ToolModes = make(map[string]connectors.ToolMode, len(b.ToolModes))
		for k, v := range b.ToolModes {
			in.ToolModes[k] = connectors.ToolMode(v)
		}
	}
	return in
}

// adminView is c as an admin sees it, with how many people connected their
// accounts.
func (a *API) adminView(ctx context.Context, c connectors.Connector) (AdminConnector, error) {
	v := a.view(c)
	if a.Vault == nil {
		return v, nil
	}
	all, err := a.Vault.ForConnector(ctx, c.ID)
	if err != nil {
		return v, problem.Internal("list connections", err)
	}
	v.Connections = len(all)
	return v, nil
}

func (a *API) view(c connectors.Connector) AdminConnector {
	v := AdminConnector{
		ID: c.ID, Rev: c.Rev, CatalogID: c.CatalogID, Slug: c.Slug, Name: c.Name, URL: c.URL, Enabled: c.Enabled,
		OAuth: AdminOAuth{
			ClientID: c.OAuth.ClientID, SecretStored: c.OAuth.SecretStored, Registration: c.OAuth.Registration,
			AuthorizeURL: c.OAuth.AuthorizeURL, TokenURL: c.OAuth.TokenURL, RevokeURL: c.OAuth.RevokeURL,
		},
		Scopes: nonNil(c.Scopes), AllowedGroups: nonNil(c.AllowedGroups), ToolModes: modes(c.ToolModes),
		Tools: make([]ToolView, 0, len(c.Tools)), RedirectURI: connectors.RedirectURI(a.PublicURL, c.Slug), UpdatedAt: c.UpdatedAt,
	}
	if p := c.OAuth.Server; p != nil {
		v.OAuth.Issuer, v.OAuth.ServerMoved = p.Issuer, p.Moved
	}
	for _, t := range c.Tools {
		v.Tools = append(v.Tools, ToolView{Name: t.Name, Description: t.Description, ReadOnlyHint: t.ReadOnlyHint})
	}
	return v
}

func modes(m map[string]connectors.ToolMode) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
