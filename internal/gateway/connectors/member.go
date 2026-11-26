package connectors

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// SignIn runs sign-ins at connectors' vendors (package oauth).
type SignIn interface {
	// Start returns the vendor's address to send person to, for subject's
	// connection (person, or "" for the company's).
	Start(ctx context.Context, actor model.Actor, c connectors.Connector, person, subject string) (string, error)
	// Complete finishes person's sign-in with what the vendor sent back.
	Complete(ctx context.Context, person, state, code, iss string) (vault.Grant, error)
}

// ToolLister lists a connector's tools at its MCP server with bearer.
type ToolLister interface {
	ListTools(ctx context.Context, c connectors.Connector, bearer string) ([]connectors.Tool, error)
}

// ConnectionsPath is the page of the UI people connect accounts at.
const ConnectionsPath = "/settings/connections"

// MyTool is one of a connector's tools as an agent sees it on /mcp.
type MyTool struct {
	Name string `json:"name" doc:"The tool's name on Tapas's MCP endpoint: slug__tool"`
	Mode string `json:"mode" enum:"read,write" doc:"read runs at once; write is proposed for you to approve"`
}

// MyConnector is a connector the caller may use, and whether they are
// connected to it.
type MyConnector struct {
	ID          string     `json:"id"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name"`
	AuthMode    string     `json:"authMode" enum:"user,shared" doc:"user: you connect your own account; shared: an admin connected one account for everyone"`
	Connected   bool       `json:"connected" doc:"A connection exists (yours, or the shared one)"`
	Status      string     `json:"status,omitempty" enum:"active,needs_reauth" doc:"needs_reauth: the vendor no longer accepts it; connect again"`
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`
	ConnectURL  string     `json:"connectUrl" doc:"The page to connect it at"`
	Tools       []MyTool   `json:"tools" doc:"Its tools, from the last snapshot an admin took"`
}

type myListOutput struct {
	Body struct {
		Items []MyConnector `json:"items"`
	}
}

// AuthorizeURL is where to send the browser to sign in at the vendor.
type AuthorizeURL struct {
	AuthorizeURL string `json:"authorizeUrl" doc:"The vendor's sign-in page; it sends the browser back to the redirect address"`
}

type authorizeOutput struct{ Body AuthorizeURL }

// CompleteInput is what the vendor sent the browser back with.
type CompleteInput struct {
	State string `json:"state" minLength:"1" maxLength:"256"`
	Code  string `json:"code" minLength:"1" maxLength:"4096"`
	Iss   string `json:"iss,omitempty" maxLength:"2048" doc:"The issuer the vendor named (RFC 9207), when it sent one"`
}

type completeInput struct{ Body CompleteInput }

type completeOutput struct {
	Body struct {
		Connector MyConnector `json:"connector"`
	}
}

// on reports whether connecting accounts works: connectors are on and this
// build has the vault and the sign-in.
func (a *API) on() bool { return a.Registry != nil && a.Vault != nil && a.SignIn != nil }

// registerMember adds the routes people connect their own accounts with.
func (a *API) registerMember(api huma.API) {
	tag := []string{"connectors"}
	huma.Register(api, huma.Operation{
		OperationID: "listConnectors", Method: http.MethodGet, Path: "/api/connectors",
		Summary:     "Connectors you may use, and whether you connected them",
		Description: "Each connector's tools are offered on /mcp as slug__tool once connected. Connect at connectUrl, in the browser.",
		Tags:        tag, Errors: []int{http.StatusUnauthorized, http.StatusConflict},
	}, func(ctx context.Context, _ *struct{}) (*myListOutput, error) {
		if a.Registry == nil || a.Vault == nil {
			return nil, off()
		}
		u := authn.User(ctx)
		all, err := a.Registry.ListEnabled(ctx)
		if err != nil {
			return nil, problem.Internal("list connectors", err)
		}
		mine, err := a.Vault.ForSubject(ctx, u.ID)
		if err != nil {
			return nil, problem.Internal("list connections", err)
		}
		out := &myListOutput{}
		out.Body.Items = []MyConnector{}
		for _, c := range all {
			if !a.mayUse(u, c) {
				continue
			}
			conn, ok := find(mine, c.ID)
			if c.AuthMode == connectors.AuthShared {
				if conn, ok, err = a.shared(ctx, c.ID); err != nil {
					return nil, problem.Internal("read a connection", err)
				}
			}
			out.Body.Items = append(out.Body.Items, a.myView(c, conn, ok))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "connectConnector", Method: http.MethodPost, Path: "/api/connectors/{id}/connect",
		Summary:     "Start connecting your account to a connector (browser session only)",
		Description: "Answers the vendor's sign-in address. The vendor sends the browser back to " + connectors.RedirectPath + ", which finishes with POST /api/connectors/oauth/complete. Only for connectors in user mode.",
		Tags:        tag, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusBadGateway},
	}, func(ctx context.Context, in *idInput) (*authorizeOutput, error) {
		if !a.on() {
			return nil, off()
		}
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		c, err := a.Registry.Get(ctx, in.ID)
		if err != nil || c.AuthMode != connectors.AuthUser || !a.mayUse(u, c) {
			return nil, notFound(err)
		}
		return a.start(ctx, c, u.ID, u.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "completeConnectorSignIn", Method: http.MethodPost, Path: "/api/connectors/oauth/complete",
		Summary:     "Finish connecting an account (browser session only)",
		Description: "Called by the page at " + connectors.RedirectPath + " with what the vendor sent back. A sign-in can be finished once, within 10 minutes, by the person who started it.",
		Tags:        tag, Security: authn.SessionOnly,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusBadGateway},
	}, func(ctx context.Context, in *completeInput) (*completeOutput, error) {
		if !a.on() {
			return nil, off()
		}
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		u := authn.User(ctx)
		g, err := a.SignIn.Complete(ctx, u.ID, in.Body.State, in.Body.Code, in.Body.Iss)
		if err != nil {
			return nil, signInFailed(err)
		}
		// Who may connect is checked again: it may have changed since the
		// sign-in started.
		if g.Subject == "" && !a.admin(u) || g.Subject != "" && !a.mayUse(u, g.Connector) {
			return nil, notFound(nil)
		}
		if err := a.Vault.Put(ctx, authn.Actor(ctx), g.Connector, g.Subject, g.Tokens); err != nil {
			return nil, problem.Internal("store a connection", err)
		}
		conn, ok, err := a.connection(ctx, g.Connector.ID, g.Subject)
		if err != nil {
			return nil, problem.Internal("read a connection", err)
		}
		out := &completeOutput{}
		out.Body.Connector = a.myView(g.Connector, conn, ok)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "disconnectConnector", Method: http.MethodDelete, Path: "/api/connectors/{id}/connection",
		Summary:     "Disconnect your account from a connector (browser session only)",
		Description: "Revokes the tokens at the vendor when it allows it, and deletes them whatever it answered.",
		Tags:        tag, Security: authn.SessionOnly, DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict},
	}, func(ctx context.Context, in *idInput) (*struct{}, error) {
		if !a.on() {
			return nil, off()
		}
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		// Anyone may drop their own connection, even when no longer allowed
		// to use the connector; someone without one who may not use it
		// learns nothing of it.
		u := authn.User(ctx)
		c, err := a.Registry.Get(ctx, in.ID)
		if err != nil || c.AuthMode != connectors.AuthUser {
			return nil, notFound(err)
		}
		err = a.disconnect(ctx, c, u.ID)
		if err != nil && !a.mayUse(u, c) {
			return nil, notFound(nil)
		}
		return nil, err
	})
}

// registerShared adds the platform admins' routes for a shared connector's
// one connection, and the tool snapshot refresh.
func (a *API) registerShared(api huma.API, base string) {
	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminConnectConnector", Method: http.MethodPost, Path: base + "/{id}/connect",
		Summary:     "Start connecting the account everyone's calls use (platform admins, browser session only)",
		Description: "Only for connectors in shared mode. Everything done through it appears at the vendor as this account: connect a service account.",
		Security:    authn.SessionOnly, Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway},
	}, func(ctx context.Context, in *idInput) (*authorizeOutput, error) {
		if !a.on() {
			return nil, off()
		}
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		c, err := a.Registry.Get(ctx, in.ID)
		if err != nil || c.AuthMode != connectors.AuthShared {
			return nil, notFound(err)
		}
		return a.start(ctx, c, authn.User(ctx).ID, "")
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminDisconnectConnector", Method: http.MethodDelete, Path: base + "/{id}/connection",
		Summary:  "Disconnect the account everyone's calls use (platform admins, browser session only)",
		Security: authn.SessionOnly, DefaultStatus: http.StatusNoContent, Errors: []int{http.StatusNotFound, http.StatusConflict},
	}, func(ctx context.Context, in *idInput) (*struct{}, error) {
		if !a.on() {
			return nil, off()
		}
		if err := authn.NoTokens(ctx); err != nil {
			return nil, err
		}
		c, err := a.Registry.Get(ctx, in.ID)
		if err != nil || c.AuthMode != connectors.AuthShared {
			return nil, notFound(err)
		}
		return nil, a.disconnect(ctx, c, "")
	})

	authn.RegisterAdmin(api, a.Roles, huma.Operation{
		OperationID: "adminRefreshConnectorTools", Method: http.MethodPost, Path: base + "/{id}/tools/refresh",
		Summary:     "Take a new snapshot of a connector's tools (platform admins)",
		Description: "Lists the tools at the connector's MCP server with the shared connection, or for a user-mode connector with your own.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict, http.StatusBadGateway},
	}, func(ctx context.Context, in *idInput) (*connectorOutput, error) {
		if !a.on() || a.Tools == nil {
			return nil, off()
		}
		c, err := a.Registry.Get(ctx, in.ID)
		if err != nil {
			return nil, failed("read the connector", err, "")
		}
		subject := authn.User(ctx).ID
		if c.AuthMode == connectors.AuthShared {
			subject = ""
		}
		bearer, err := a.Vault.Token(ctx, c, subject)
		if errors.Is(err, connectors.ErrNotConnected) {
			return nil, problem.New(http.StatusConflict, reasons.Msg(reasons.MsgConnectorNotConnected, c.Name, a.connectURL()))
		}
		if err != nil {
			return nil, problem.Internal("open a connection", err)
		}
		tools, err := a.Tools.ListTools(ctx, c, bearer)
		if err != nil {
			return nil, problem.New(http.StatusBadGateway, reasons.Msg(reasons.MsgServerError))
		}
		if c, err = a.Registry.SetTools(ctx, authn.Actor(ctx), c.ID, tools); err != nil {
			return nil, failed("save the tools", err, "")
		}
		v, err := a.adminView(ctx, c)
		if err != nil {
			return nil, err
		}
		return &connectorOutput{Body: v}, nil
	})
}

func (a *API) start(ctx context.Context, c connectors.Connector, person, subject string) (*authorizeOutput, error) {
	u, err := a.SignIn.Start(ctx, authn.Actor(ctx), c, person, subject)
	if err != nil {
		return nil, signInFailed(err)
	}
	return &authorizeOutput{Body: AuthorizeURL{AuthorizeURL: u}}, nil
}

func (a *API) disconnect(ctx context.Context, c connectors.Connector, subject string) error {
	err := a.Vault.Delete(ctx, authn.Actor(ctx), c, subject)
	if errors.Is(err, connectors.ErrNotConnected) {
		return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgConnectorNotConnected, c.Name, a.connectURL()))
	}
	if err != nil {
		return problem.Internal("disconnect", err)
	}
	return nil
}

// mayUse reports whether p may use c (authz.ActUseConnector).
func (a *API) mayUse(p authz.Principal, c connectors.Connector) bool {
	return authz.Decide(p, authz.RolesOf(p, a.Roles), authz.ActUseConnector, authz.Object{
		Connector: &authz.ConnectorAsk{Enabled: c.Enabled, Groups: c.AllowedGroups},
	}).Allowed
}

// admin reports whether p is a platform admin.
func (a *API) admin(p authz.Principal) bool {
	return authz.Decide(p, authz.RolesOf(p, a.Roles), authz.ActAdminConsole, authz.Object{}).Allowed
}

func (a *API) connectURL() string { return strings.TrimRight(a.PublicURL, "/") + ConnectionsPath }

// connection is subject's connection to the connector with id, and whether
// there is one.
func (a *API) connection(ctx context.Context, id, subject string) (vault.Connection, bool, error) {
	var all []vault.Connection
	var err error
	if subject == "" {
		all, err = a.Vault.ForConnector(ctx, id)
	} else {
		all, err = a.Vault.ForSubject(ctx, subject)
	}
	if err != nil {
		return vault.Connection{}, false, err
	}
	for _, c := range all {
		if c.ConnectorID == id && c.Subject == subject {
			return c, true, nil
		}
	}
	return vault.Connection{}, false, nil
}

// shared is the company-wide connection to the connector with id.
func (a *API) shared(ctx context.Context, id string) (vault.Connection, bool, error) {
	return a.connection(ctx, id, "")
}

func find(all []vault.Connection, connectorID string) (vault.Connection, bool) {
	for _, c := range all {
		if c.ConnectorID == connectorID {
			return c, true
		}
	}
	return vault.Connection{}, false
}

func (a *API) myView(c connectors.Connector, conn vault.Connection, connected bool) MyConnector {
	v := MyConnector{
		ID: c.ID, Slug: c.Slug, Name: c.Name, AuthMode: string(c.AuthMode), Connected: connected,
		ConnectURL: a.connectURL(), Tools: []MyTool{},
	}
	if connected {
		at := conn.ConnectedAt
		v.Status, v.ConnectedAt = conn.Status, &at
	}
	for _, t := range c.Tools {
		mode := connectors.ModeOf(c.ToolModes, t.Name)
		name, ok := connectors.JoinTool(c.Slug, t.Name)
		if mode == connectors.ToolOff || !ok {
			continue
		}
		v.Tools = append(v.Tools, MyTool{Name: name, Mode: string(mode)})
	}
	return v
}

// notFound is the answer for a connector the caller may not see or use,
// or for err from reading it.
func notFound(err error) error {
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return problem.Internal("read the connector", err)
	}
	return problem.New(http.StatusNotFound, reasons.Msg(reasons.MsgConnectorNotFound))
}

// signInFailed is the answer for err from a sign-in.
func signInFailed(err error) error {
	var oe *connectors.OAuthError
	switch {
	case errors.Is(err, connectors.ErrSignInExpired):
		return problem.New(http.StatusBadRequest, reasons.Msg(reasons.MsgConnectorSignInExpired))
	case errors.Is(err, connectors.ErrNeedsClient):
		return problem.New(http.StatusConflict, reasons.Msg(reasons.MsgConnectorNeedsOAuthClient))
	case errors.As(err, &oe):
		return problem.New(http.StatusBadGateway, reasons.Msg(reasons.MsgConnectorSignInFailed, oe.Code))
	case errors.Is(err, store.ErrNotFound):
		return notFound(nil)
	}
	return problem.Internal("sign in to a connector", err)
}
