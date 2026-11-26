// Package vault keeps the tokens people's connected accounts gave Tapas:
// it seals them with the gateway's sessions key, bound to their row and
// column (as package registry seals the OAuth client secret), opens them
// for the broker, refreshes them before they expire, and revokes them on
// disconnect. Reaching the authorization server is left to an OAuth
// implementation (package oauth), so this package carries no MCP library.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// Tokens is what an authorization server granted. It never prints: %v and
// slog show [redacted].
type Tokens struct {
	Access, Refresh string
	// Expiry is when Access stops working; zero when the server gave none.
	Expiry time.Time
	// Scopes are what was granted, when the server said.
	Scopes []string
}

const redacted = "[redacted]"

// LogValue keeps tokens out of logs.
func (Tokens) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format keeps tokens out of anything printed with fmt.
func (Tokens) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// Grant is a finished sign-in: the connector, whose connection it is (the
// person, or "" for the company's) and the tokens, ready for Put.
type Grant struct {
	Connector connectors.Connector
	Subject   string
	Tokens    Tokens
}

// OAuth reaches a connector's authorization server.
type OAuth interface {
	// Refresh trades refresh for new tokens. A refusal for good is a
	// *connectors.OAuthError with code invalid_grant.
	Refresh(ctx context.Context, c connectors.Connector, refresh string) (Tokens, error)
	// Revoke revokes token (RFC 7009); hint is refresh_token or
	// access_token. ErrNoRevocation when the server has no endpoint.
	Revoke(ctx context.Context, c connectors.Connector, token, hint string) error
}

// ErrNoRevocation is an authorization server without a revocation endpoint.
var ErrNoRevocation = errors.New("vault: the authorization server has no revocation endpoint")

// Timing of refreshes and revocation.
const (
	// RefreshBefore is how long before it expires an access token is
	// refreshed.
	RefreshBefore = time.Minute
	// ClaimFor is how long one replica holds the right to refresh.
	ClaimFor = 30 * time.Second
	// WaitFor is how long a caller waits for another replica's refresh.
	WaitFor = 10 * time.Second
	// RevokeTimeout bounds revocation, which is best effort.
	RevokeTimeout = 5 * time.Second
)

// Config wires a Vault. DB and Seal are required.
type Config struct {
	DB *store.Store
	// Seal is the gateway's sessions key; it must survive restarts.
	Seal tink.AEAD
	// OAuth refreshes and revokes; nil refreshes nothing (an expired token
	// needs connecting again) and revokes nothing.
	OAuth OAuth
	// Now is the clock; nil is the real one.
	Now func() time.Time
	// Poll is how often a waiting caller looks for another replica's
	// refresh; zero is 100ms.
	Poll time.Duration
}

// Vault is the connected accounts' tokens.
type Vault struct {
	c Config

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New returns the vault over c. It panics without c.DB or c.Seal.
func New(c Config) *Vault {
	if c.DB == nil || c.Seal == nil {
		panic("vault.New: Config.DB and Config.Seal are required")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Poll == 0 {
		c.Poll = 100 * time.Millisecond
	}
	return &Vault{c: c, locks: map[string]*sync.Mutex{}}
}

var _ connectors.TokenSource = (*Vault)(nil)

const table = "connector_connections"

func ad(id, column string) []byte { return connectors.SealContext(table, id, column) }

func (v *Vault) seal(id string, t Tokens) (access, refresh []byte, err error) {
	if access, err = v.c.Seal.Encrypt([]byte(t.Access), ad(id, "access_sealed")); err != nil {
		return nil, nil, err
	}
	if t.Refresh == "" {
		return access, nil, nil
	}
	refresh, err = v.c.Seal.Encrypt([]byte(t.Refresh), ad(id, "refresh_sealed"))
	return access, refresh, err
}

// open is the clear token in sealed, sealed for column of the row with id;
// "" for none.
func (v *Vault) open(id, column string, sealed []byte) (string, error) {
	if sealed == nil {
		return "", nil
	}
	b, err := v.c.Seal.Decrypt(sealed, ad(id, column))
	if err != nil {
		return "", errors.New("vault: a stored token does not open with this gateway's sessions key")
	}
	return string(b), nil
}

// Put stores t as subject's connection to c (subject "" for the company's),
// replacing any there was. actor connected it. When c is no longer at
// c.Rev (an admin changed it while the tokens were obtained) nothing is
// stored and the error is store.ErrConflict.
func (v *Vault) Put(ctx context.Context, actor model.Actor, c connectors.Connector, subject string, t Tokens) error {
	if t.Access == "" {
		return errors.New("vault: no access token to store")
	}
	_, err := v.c.DB.ConnectorConnections().Put(ctx, actor, store.ConnectorConnection{
		ConnectorID: c.ID, Subject: subject, ConnectedBy: actor.Person, Resource: c.URL, Scopes: t.Scopes, ExpiresAt: t.Expiry,
	}, c.Rev, func(id string) ([]byte, []byte, error) { return v.seal(id, t) })
	return err
}

// Token is the access token of subject's connection to c, refreshed first
// when it expires within RefreshBefore. An error wrapping
// connectors.ErrNotConnected means there is no usable connection: none, one
// for another address, or one that needs connecting again.
func (v *Vault) Token(ctx context.Context, c connectors.Connector, subject string) (string, error) {
	row, err := v.usable(ctx, c, subject)
	if err != nil {
		return "", err
	}
	if v.fresh(row) {
		return v.open(row.ID, "access_sealed", row.AccessSealed)
	}
	return v.refresh(ctx, c, row, "")
}

// Invalidate refreshes subject's connection to c because the connector
// refused bearer; nothing is done when the stored token is already
// another one.
func (v *Vault) Invalidate(ctx context.Context, c connectors.Connector, subject, bearer string) error {
	row, err := v.usable(ctx, c, subject)
	if err != nil {
		return err
	}
	_, err = v.refresh(ctx, c, row, bearer)
	return err
}

// usable is subject's connection to c when it may be used: issued for c's
// address and not waiting to be connected again.
func (v *Vault) usable(ctx context.Context, c connectors.Connector, subject string) (store.ConnectorConnection, error) {
	row, err := v.c.DB.ConnectorConnections().Get(ctx, c.ID, subject)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return row, connectors.ErrNotConnected
	case err != nil:
		return row, err
	}
	// A token is only ever sent to the address it was issued for.
	if row.Resource != c.URL || row.Status != store.ConnectionActive {
		return row, connectors.ErrNotConnected
	}
	return row, nil
}

func (v *Vault) fresh(row store.ConnectorConnection) bool {
	return row.ExpiresAt.IsZero() || row.ExpiresAt.After(v.c.Now().Add(RefreshBefore))
}

// lock is the in-process lock of the connection with id, so callers on one
// replica refresh it once.
func (v *Vault) lock(id string) *sync.Mutex {
	v.mu.Lock()
	defer v.mu.Unlock()
	l, ok := v.locks[id]
	if !ok {
		l = &sync.Mutex{}
		v.locks[id] = l
	}
	return l
}

// refresh gets row's connection a new access token, unless the one stored
// is already fresh and not refused (refused is the bearer a connector
// refused, "" for none). One replica refreshes at a time (the database
// claim); the others wait for its result for up to WaitFor.
func (v *Vault) refresh(ctx context.Context, c connectors.Connector, row store.ConnectorConnection, refused string) (string, error) {
	l := v.lock(row.ID)
	l.Lock()
	defer l.Unlock()
	// The wait is bounded by the real clock, whatever Now says.
	deadline := time.Now().Add(WaitFor)
	for {
		cur, err := v.usable(ctx, c, row.Subject)
		if err != nil {
			return "", err
		}
		access, err := v.open(cur.ID, "access_sealed", cur.AccessSealed)
		if err != nil {
			return "", err
		}
		if access != refused && v.fresh(cur) && (cur.Rev != row.Rev || refused != "") {
			return access, nil
		}
		now := v.c.Now()
		won, err := v.c.DB.ConnectorConnections().ClaimRefresh(ctx, cur.ID, cur.Rev, now, now.Add(ClaimFor))
		if err != nil {
			return "", err
		}
		if won {
			return v.renew(ctx, c, cur)
		}
		if !time.Now().Before(deadline) {
			return "", errors.New("vault: another replica is still refreshing the token")
		}
		row = cur
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(v.c.Poll):
		}
	}
}

// renew refreshes row's tokens, holding its claim. A refusal for good, or
// no refresh token, marks it needs_reauth.
func (v *Vault) renew(ctx context.Context, c connectors.Connector, row store.ConnectorConnection) (string, error) {
	conns := v.c.DB.ConnectorConnections()
	refresh, err := v.open(row.ID, "refresh_sealed", row.RefreshSealed)
	if err != nil {
		return "", v.release(ctx, row, err)
	}
	if refresh == "" || v.c.OAuth == nil {
		return "", v.needsReauth(ctx, c, row, "no_refresh_token")
	}
	t, err := v.c.OAuth.Refresh(ctx, c, refresh)
	if connectors.IsInvalidGrant(err) {
		return "", v.needsReauth(ctx, c, row, "invalid_grant")
	}
	if err != nil {
		return "", v.release(ctx, row, err)
	}
	if t.Access == "" {
		return "", v.release(ctx, row, errors.New("vault: the refresh gave no access token"))
	}
	if t.Refresh == "" {
		t.Refresh = refresh
	}
	access, sealedRefresh, err := v.seal(row.ID, t)
	if err != nil {
		return "", v.release(ctx, row, err)
	}
	if _, err := conns.SetTokens(ctx, row.ID, row.Rev, access, sealedRefresh, t.Expiry); err != nil {
		// errors.Is(err, store.ErrConflict): the person connected again
		// meanwhile; that newer connection stays.
		return "", err
	}
	return t.Access, nil
}

// release gives up row's refresh claim after a failure that may pass, and
// returns err.
func (v *Vault) release(ctx context.Context, row store.ConnectorConnection, err error) error {
	if rerr := v.c.DB.ConnectorConnections().SetStatus(ctx, row.ID, store.ConnectionActive); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
		slog.Error("release a connector token's refresh claim", "err", rerr)
	}
	return err
}

// needsReauth marks row as needing connecting again, audits why (a code,
// never a token) and answers not connected.
func (v *Vault) needsReauth(ctx context.Context, c connectors.Connector, row store.ConnectorConnection, reason string) error {
	if err := v.c.DB.ConnectorConnections().SetStatus(ctx, row.ID, store.ConnectionNeedsReauth); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	actor, via := store.ActorStrings(model.Actor{System: model.SystemGateway})
	detail, _ := json.Marshal(map[string]string{"reason": reason})
	if err := v.c.DB.Audit().Append(ctx, store.AuditEvent{
		Actor: actor, Via: via, Action: "connector.refresh_failed", ObjectKind: "connector", ObjectName: c.Slug,
		ObjectID: c.ID, ObjectOwner: row.Subject, Allowed: true, Detail: detail,
	}); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", connectors.ErrNotConnected, reason)
}

// Delete disconnects subject from c: it revokes the refresh token, then the
// access token, at the authorization server (best effort, RevokeTimeout),
// then deletes the connection whatever revocation said. How revocation went
// is on the audit row. Without a connection it is ErrNotConnected.
func (v *Vault) Delete(ctx context.Context, actor model.Actor, c connectors.Connector, subject string) error {
	row, err := v.c.DB.ConnectorConnections().Get(ctx, c.ID, subject)
	if errors.Is(err, store.ErrNotFound) {
		return connectors.ErrNotConnected
	}
	if err != nil {
		return err
	}
	outcome := v.revoke(ctx, c, row)
	err = v.c.DB.ConnectorConnections().Delete(ctx, actor, c.ID, subject, map[string]string{"revoke": outcome})
	if errors.Is(err, store.ErrNotFound) {
		return connectors.ErrNotConnected
	}
	return err
}

// DeleteForConnector disconnects everyone from c, as Delete does each: when
// c is removed, or its address, endpoints, client or auth mode change.
func (v *Vault) DeleteForConnector(ctx context.Context, actor model.Actor, c connectors.Connector) error {
	rows, err := v.c.DB.ConnectorConnections().ForConnector(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := v.Delete(ctx, actor, c, row.Subject); err != nil && !errors.Is(err, connectors.ErrNotConnected) {
			return err
		}
	}
	return nil
}

// revoke revokes row's tokens and says how it went: ok, failed, none (the
// server has no revocation endpoint) or skipped (no OAuth, a token for
// another address, or one that does not open).
func (v *Vault) revoke(ctx context.Context, c connectors.Connector, row store.ConnectorConnection) string {
	if v.c.OAuth == nil || row.Resource != c.URL {
		return "skipped"
	}
	access, err := v.open(row.ID, "access_sealed", row.AccessSealed)
	if err != nil {
		return "skipped"
	}
	refresh, err := v.open(row.ID, "refresh_sealed", row.RefreshSealed)
	if err != nil {
		return "skipped"
	}
	ctx, cancel := context.WithTimeout(ctx, RevokeTimeout)
	defer cancel()
	outcome := "ok"
	for _, t := range []struct{ token, hint string }{{refresh, "refresh_token"}, {access, "access_token"}} {
		if t.token == "" {
			continue
		}
		err := v.c.OAuth.Revoke(ctx, c, t.token, t.hint)
		switch {
		case errors.Is(err, ErrNoRevocation):
			return "none"
		case err != nil:
			slog.Warn("revoke a connector token", "connector", c.Slug, "err", err)
			outcome = "failed"
		}
	}
	return outcome
}

// Connection is a connection as people and admins see it: never a token.
type Connection struct {
	ConnectorID, Subject, ConnectedBy, Status string
	ConnectedAt                               time.Time
}

func view(rows []store.ConnectorConnection, err error) ([]Connection, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Connection, 0, len(rows))
	for _, r := range rows {
		out = append(out, Connection{ConnectorID: r.ConnectorID, Subject: r.Subject, ConnectedBy: r.ConnectedBy, Status: r.Status, ConnectedAt: r.CreatedAt})
	}
	return out, nil
}

// ForSubject is subject's connections.
func (v *Vault) ForSubject(ctx context.Context, subject string) ([]Connection, error) {
	return view(v.c.DB.ConnectorConnections().ForSubject(ctx, subject))
}

// ForConnector is every connection to the connector with id.
func (v *Vault) ForConnector(ctx context.Context, id string) ([]Connection, error) {
	return view(v.c.DB.ConnectorConnections().ForConnector(ctx, id))
}

// RunStateJanitor deletes expired OAuth sign-ins every interval until ctx
// ends.
func RunStateJanitor(ctx context.Context, db *store.Store, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := db.ConnectorConnections().SweepOAuthStates(ctx, time.Now()); err != nil && ctx.Err() == nil {
				slog.Warn("sweep connector sign-ins", "err", err)
			}
		}
	}
}
