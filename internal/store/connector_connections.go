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

// ConnectorConnection is one row of the token vault: the account a person
// (Subject), or the company (Subject ""), connected to a connector.
// AccessSealed and RefreshSealed are the tokens as the gateway sealed them
// for this row's id (RefreshSealed nil: none); the clear tokens are never
// stored. Resource is the MCP address they were issued for. ExpiresAt is
// zero when the vendor gave no expiry. RefreshingUntil is the end of a
// refresh claim. Rev is new on every write of the tokens.
type ConnectorConnection struct {
	ID, ConnectorID, Subject, ConnectedBy, Resource string
	AccessSealed, RefreshSealed                     []byte
	Scopes                                          []string
	Status                                          string
	ExpiresAt, RefreshingUntil                      time.Time
	Rev                                             string
	CreatedAt, UpdatedAt                            time.Time
}

// The states of a ConnectorConnection.
const (
	ConnectionActive = "active"
	// ConnectionNeedsReauth: the vendor refused the refresh token (or there
	// is none); the person must connect again.
	ConnectionNeedsReauth = "needs_reauth"
)

// ConnectorOAuthState is an OAuth sign-in in progress. StateHash is the
// SHA-256 of the state only the browser holds; VerifierSealed the sealed
// PKCE verifier. Person started it and is the only one who may finish it,
// for Subject (Person, or "" for a company-wide connection). Issuer is the
// authorization server that must name itself in the answer (RFC 9207), ""
// when it does not say it will.
type ConnectorOAuthState struct {
	StateHash                                  []byte
	ConnectorID, ConnectorRev, Person, Subject string
	VerifierSealed                             []byte
	RedirectURI, Issuer                        string
	CreatedAt, ExpiresAt                       time.Time
}

// ConnectorConnections is the token vault's tables. Connecting and
// disconnecting are audited (connector.connect, connector.disconnect);
// refresh claims, new tokens from a refresh, status changes and OAuth states
// are plumbing and are not.
type ConnectorConnections struct{ s *Store }

// ConnectorConnections returns the token vault's tables.
func (s *Store) ConnectorConnections() *ConnectorConnections { return &ConnectorConnections{s} }

// sealTokensFunc gets a new row's id and returns the access and refresh
// tokens sealed for it (refresh nil: none).
type sealTokensFunc func(id string) (access, refresh []byte, err error)

// connectionAudit is the audit row of a connect or disconnect: the
// connector's slug and id and whose connection it is.
func connectionAudit(action string, c Connector, subject string, detail any) *auditRow {
	return &auditRow{action: action, kind: connectorKind, name: c.Slug, id: c.ID, owner: subject, detail: detail}
}

// Put stores c as the connection of c.Subject to c.ConnectorID, replacing
// any there was, under a new id and rev; seal gets the new id. It is
// active, with no refresh claim. A missing connector is ErrNotFound, one
// whose revision is no longer connectorRev (the revision the tokens were
// obtained under) ErrConflict, checked in the same transaction. It is
// audited.
func (x *ConnectorConnections) Put(ctx context.Context, actor model.Actor, c ConnectorConnection, connectorRev string, seal sealTokensFunc) (ConnectorConnection, error) {
	c.ID, c.Rev, c.Status = newID(), newID(), ConnectionActive
	var err error
	if c.AccessSealed, c.RefreshSealed, err = seal(c.ID); err != nil {
		return ConnectorConnection{}, err
	}
	scopes, err := marshal(nonNil(c.Scopes), "[]")
	if err != nil {
		return ConnectorConnection{}, err
	}
	t := dbtime.From(now())
	var out ConnectorConnection
	err = x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		locked, err := q.LockConnectorRev(ctx, sqlcgen.LockConnectorRevParams{ID: c.ConnectorID, Rev: connectorRev})
		if err != nil {
			return effect{}, err
		}
		conn, err := readConnector(q.GetConnector(ctx, c.ConnectorID))
		if err != nil {
			return effect{}, err
		}
		if locked == 0 {
			return effect{}, ErrConflict
		}
		if err := q.UpsertConnectorConnection(ctx, sqlcgen.UpsertConnectorConnectionParams{
			ID: c.ID, ConnectorID: c.ConnectorID, Subject: c.Subject, ConnectedBy: c.ConnectedBy, Resource: c.Resource,
			AccessSealed: c.AccessSealed, RefreshSealed: c.RefreshSealed, Scopes: scopes, Status: c.Status,
			ExpiresAt: nullTime(c.ExpiresAt), Rev: c.Rev, CreatedAt: t, UpdatedAt: t,
		}); err != nil {
			return effect{}, err
		}
		out, err = readConnection(q.GetConnectorConnection(ctx, sqlcgen.GetConnectorConnectionParams{ConnectorID: c.ConnectorID, Subject: c.Subject}))
		return effect{audit: connectionAudit("connector.connect", conn, c.Subject, nil)}, err
	})
	return out, err
}

// Get returns the connection of subject to the connector with connectorID,
// or ErrNotFound.
func (x *ConnectorConnections) Get(ctx context.Context, connectorID, subject string) (ConnectorConnection, error) {
	return readConnection(x.s.q.GetConnectorConnection(ctx, sqlcgen.GetConnectorConnectionParams{ConnectorID: connectorID, Subject: subject}))
}

// ForSubject returns subject's connections.
func (x *ConnectorConnections) ForSubject(ctx context.Context, subject string) ([]ConnectorConnection, error) {
	return connectionList(x.s.q.ListConnectorConnectionsForSubject(ctx, subject))
}

// ForConnector returns every connection to the connector with connectorID.
func (x *ConnectorConnections) ForConnector(ctx context.Context, connectorID string) ([]ConnectorConnection, error) {
	return connectionList(x.s.q.ListConnectorConnectionsForConnector(ctx, connectorID))
}

// ClaimRefresh claims the right to refresh the connection with id until
// until, when its rev is still rev and nobody else's claim holds at now.
// It reports whether this caller won.
func (x *ConnectorConnections) ClaimRefresh(ctx context.Context, id, rev string, now, until time.Time) (bool, error) {
	n, err := x.s.q.ClaimConnectorRefresh(ctx, sqlcgen.ClaimConnectorRefreshParams{
		RefreshingUntil: dbtime.From(until), ID: id, Rev: rev, RefreshingUntil_2: dbtime.From(now),
	})
	return n == 1, err
}

// SetTokens stores refreshed tokens (sealed for the row's id) on the
// connection with id when its rev is still rev (ErrConflict otherwise),
// under a new rev, active and with the claim released. expires zero is no
// expiry.
func (x *ConnectorConnections) SetTokens(ctx context.Context, id, rev string, access, refresh []byte, expires time.Time) (ConnectorConnection, error) {
	var out ConnectorConnection
	err := x.s.inTx(ctx, func(q *sqlcgen.Queries) error {
		n, err := q.SetConnectorTokens(ctx, sqlcgen.SetConnectorTokensParams{
			AccessSealed: access, RefreshSealed: refresh, ExpiresAt: nullTime(expires), Status: ConnectionActive,
			Rev: newID(), UpdatedAt: dbtime.From(now()), ID: id, Rev_2: rev,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
		out, err = readConnection(q.GetConnectorConnectionByID(ctx, id))
		return err
	})
	return out, err
}

// SetStatus sets the status of the connection with id and releases its
// refresh claim; ErrNotFound when there is no such row.
func (x *ConnectorConnections) SetStatus(ctx context.Context, id, status string) error {
	n, err := x.s.q.SetConnectorConnectionStatus(ctx, sqlcgen.SetConnectorConnectionStatusParams{Status: status, UpdatedAt: dbtime.From(now()), ID: id})
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// Delete removes subject's connection to the connector with connectorID
// (ErrNotFound when there is none) and records it on the audit log with
// detail (names and outcomes only, never a token).
func (x *ConnectorConnections) Delete(ctx context.Context, actor model.Actor, connectorID, subject string, detail any) error {
	return x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		conn, err := readConnector(q.GetConnector(ctx, connectorID))
		if err != nil {
			return effect{}, err
		}
		n, err := q.DeleteConnectorConnection(ctx, sqlcgen.DeleteConnectorConnectionParams{ConnectorID: connectorID, Subject: subject})
		if err != nil {
			return effect{}, err
		}
		if n == 0 {
			return effect{}, ErrNotFound
		}
		return effect{audit: connectionAudit("connector.disconnect", conn, subject, detail)}, nil
	})
}

// DeleteForSubject removes every connection of the person subject (never
// the company-wide ones), without revoking them, and records how many on
// the audit log: for a person who is removed.
func (x *ConnectorConnections) DeleteForSubject(ctx context.Context, actor model.Actor, subject string) (int64, error) {
	var n int64
	err := x.s.mutate(ctx, actor, func(q *sqlcgen.Queries) (effect, error) {
		var err error
		if n, err = q.DeleteConnectorConnectionsForSubject(ctx, subject); err != nil || n == 0 {
			return effect{}, err
		}
		return effect{audit: &auditRow{action: "connector.disconnect", kind: connectorKind, owner: subject, detail: map[string]int64{"connections": n}}}, nil
	})
	return n, err
}

// StartOAuth stores a sign-in in progress.
func (x *ConnectorConnections) StartOAuth(ctx context.Context, st ConnectorOAuthState) error {
	return x.s.q.InsertConnectorOAuthState(ctx, sqlcgen.InsertConnectorOAuthStateParams{
		StateHash: st.StateHash, ConnectorID: st.ConnectorID, ConnectorRev: st.ConnectorRev, Person: st.Person, Subject: st.Subject,
		VerifierSealed: st.VerifierSealed, RedirectUri: st.RedirectURI, Issuer: st.Issuer,
		CreatedAt: dbtime.From(st.CreatedAt), ExpiresAt: dbtime.From(st.ExpiresAt),
	})
}

// TakeOAuthState removes and returns the sign-in whose state hashes to
// hash, or ErrNotFound: a state is used once. The caller checks who
// finishes it and its expiry.
func (x *ConnectorConnections) TakeOAuthState(ctx context.Context, hash []byte) (ConnectorOAuthState, error) {
	r, err := x.s.q.TakeConnectorOAuthState(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectorOAuthState{}, ErrNotFound
	}
	if err != nil {
		return ConnectorOAuthState{}, err
	}
	return ConnectorOAuthState{
		StateHash: r.StateHash, ConnectorID: r.ConnectorID, ConnectorRev: r.ConnectorRev, Person: r.Person, Subject: r.Subject,
		VerifierSealed: r.VerifierSealed, RedirectURI: r.RedirectUri, Issuer: r.Issuer,
		CreatedAt: r.CreatedAt.Time(), ExpiresAt: r.ExpiresAt.Time(),
	}, nil
}

// SweepOAuthStates removes the sign-ins that expired before t.
func (x *ConnectorConnections) SweepOAuthStates(ctx context.Context, t time.Time) (int64, error) {
	return x.s.q.SweepConnectorOAuthStates(ctx, dbtime.From(t))
}

func readConnection(r sqlcgen.ConnectorConnection, err error) (ConnectorConnection, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectorConnection{}, ErrNotFound
	}
	if err != nil {
		return ConnectorConnection{}, err
	}
	c := ConnectorConnection{
		ID: r.ID, ConnectorID: r.ConnectorID, Subject: r.Subject, ConnectedBy: r.ConnectedBy, Resource: r.Resource,
		AccessSealed: r.AccessSealed, RefreshSealed: r.RefreshSealed, Scopes: []string{}, Status: r.Status,
		ExpiresAt: timeOf(r.ExpiresAt), RefreshingUntil: r.RefreshingUntil.Time(), Rev: r.Rev,
		CreatedAt: r.CreatedAt.Time(), UpdatedAt: r.UpdatedAt.Time(),
	}
	if err := unmarshal(r.Scopes, &c.Scopes); err != nil {
		return ConnectorConnection{}, err
	}
	return c, nil
}

func connectionList(rows []sqlcgen.ConnectorConnection, err error) ([]ConnectorConnection, error) {
	if err != nil {
		return nil, err
	}
	out := make([]ConnectorConnection, 0, len(rows))
	for _, r := range rows {
		c, err := readConnection(r, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}
