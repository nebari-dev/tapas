// Package connectors is the HTTP API of connectors: the platform admins'
// routes that add, change and remove them. The gateway registers it only in
// a binary built without the tag local.
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
	Roles    config.Roles
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
	AuthMode      string            `json:"authMode,omitempty" enum:"user,shared" doc:"user: each person connects their own account; shared: one account an admin connects, used for everyone allowed"`
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
	AuthMode      string            `json:"authMode" enum:"user,shared"`
	Enabled       bool              `json:"enabled"`
	OAuth         AdminOAuth        `json:"oauth"`
	Scopes        []string          `json:"scopes"`
	AllowedGroups []string          `json:"allowedGroups"`
	ToolModes     map[string]string `json:"toolModes"`
	Tools         []ToolView        `json:"tools" doc:"The last snapshot of the server's tool list"`
	RedirectURI   string            `json:"redirectUri,omitempty" doc:"Paste this at the vendor as the OAuth redirect address"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// CatalogEntry is one entry of the built-in catalogue.
type CatalogEntry struct {
	ID             string            `json:"id"`
	Slug           string            `json:"slug"`
	Name           string            `json:"name"`
	URL            string            `json:"url" doc:"The vendor's hosted MCP server; empty: the admin fills it in"`
	AuthMode       string            `json:"authMode" enum:"user,shared"`
	Scopes         []string          `json:"scopes"`
	ToolModes      map[string]string `json:"toolModes"`
	OAuthEndpoints string            `json:"oauthEndpoints" enum:"discovered,static"`
	OAuth          catalog.Endpoints `json:"oauth"`
	Setup          string            `json:"setup" doc:"What to do at the vendor"`
	Docs           string            `json:"docs" doc:"The vendor's documentation of its MCP server"`
}

type catalogOutput struct {
	Body struct {
		Items []CatalogEntry `json:"items"`
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
		out.Body.Items = []CatalogEntry{}
		for _, e := range catalog.All() {
			out.Body.Items = append(out.Body.Items, CatalogEntry{
				ID: e.ID, Slug: e.Slug, Name: e.Name, URL: e.URL, AuthMode: string(e.AuthMode), Scopes: nonNil(e.Scopes),
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
			out.Body.Items = append(out.Body.Items, a.view(c))
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
		return &createOutput{Status: http.StatusCreated, Body: a.view(c)}, nil
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
		return &connectorOutput{Body: a.view(c)}, nil
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminUpdateConnector", Method: http.MethodPut, Path: base + "/{id}",
		Summary:     "Change a connector (platform admins)",
		Description: "rev must be the connector's current rev (409 otherwise). A new address origin, new OAuth endpoints or a new client id need the client secret again when one is stored, and disconnect everyone who connected.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, in *updateInput) (*connectorOutput, error) {
		if a.Registry == nil {
			return nil, off()
		}
		c, err := a.Registry.Update(ctx, authn.Actor(ctx), in.ID, registryInput(in.Body))
		if err != nil {
			return nil, failed("change the connector", err, "")
		}
		return &connectorOutput{Body: a.view(c)}, nil
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
		CatalogID: b.CatalogID, Slug: b.Slug, Name: b.Name, URL: b.URL, AuthMode: connectors.AuthMode(b.AuthMode), Enabled: b.Enabled,
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

func (a *API) view(c connectors.Connector) AdminConnector {
	v := AdminConnector{
		ID: c.ID, Rev: c.Rev, CatalogID: c.CatalogID, Slug: c.Slug, Name: c.Name, URL: c.URL, AuthMode: string(c.AuthMode), Enabled: c.Enabled,
		OAuth: AdminOAuth{
			ClientID: c.OAuth.ClientID, SecretStored: c.OAuth.SecretStored, Registration: c.OAuth.Registration,
			AuthorizeURL: c.OAuth.AuthorizeURL, TokenURL: c.OAuth.TokenURL, RevokeURL: c.OAuth.RevokeURL,
		},
		Scopes: nonNil(c.Scopes), AllowedGroups: nonNil(c.AllowedGroups), ToolModes: modes(c.ToolModes),
		Tools: make([]ToolView, 0, len(c.Tools)), RedirectURI: connectors.RedirectURI(a.PublicURL), UpdatedAt: c.UpdatedAt,
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
