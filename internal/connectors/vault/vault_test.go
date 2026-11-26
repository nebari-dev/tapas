package vault_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	ctx   = context.Background()
	alice = model.Actor{Person: "alice"}
)

// fakeOAuth stands for the connector's authorization server: refresh
// rotates both tokens, and can be told to refuse for good or to wait.
type fakeOAuth struct {
	mu        sync.Mutex
	refreshes []string
	revoked   []string
	n         int
	refuse    bool
	noRotate  bool
	failRev   error
	gate      chan struct{}
}

func (f *fakeOAuth) Refresh(_ context.Context, _ connectors.Connector, refresh string) (vault.Tokens, error) {
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, refresh)
	if f.refuse {
		return vault.Tokens{}, fmt.Errorf("refresh: %w", connectors.NewOAuthError("invalid_grant"))
	}
	f.n++
	t := vault.Tokens{Access: fmt.Sprintf("access-%d", f.n), Expiry: time.Now().Add(time.Hour)}
	if !f.noRotate {
		t.Refresh = fmt.Sprintf("refresh-%d", f.n)
	}
	return t, nil
}

func (f *fakeOAuth) Revoke(_ context.Context, _ connectors.Connector, token, hint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, hint+":"+token)
	return f.failRev
}

func (f *fakeOAuth) refreshCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.refreshes)
}

type fixture struct {
	db    *store.Store
	v     *vault.Vault
	oauth *fakeOAuth
	conn  connectors.Connector
	cfg   vault.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := storetest.New(t)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.Connectors().Create(ctx, alice, store.Connector{
		Slug: "fake", Name: "Fake", URL: "https://mcp.example.org/mcp", AuthMode: "user", Enabled: true,
	}, func(string) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{db: db, oauth: &fakeOAuth{}}
	f.conn = connectors.Connector{ID: row.ID, Slug: row.Slug, Name: row.Name, URL: row.URL, AuthMode: connectors.AuthUser, Rev: row.Rev}
	f.cfg = vault.Config{DB: db, Seal: key, OAuth: f.oauth}
	f.v = vault.New(f.cfg)
	return f
}

func (f *fixture) put(t *testing.T, subject string, tok vault.Tokens) {
	t.Helper()
	if err := f.v.Put(ctx, alice, f.conn, subject, tok); err != nil {
		t.Fatal(err)
	}
}

// Tokens are stored sealed: neither is readable in the table, and each
// opens again through the vault.
func TestTokensAreSealedAtRest(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "access-sentinel-1", Refresh: "refresh-sentinel-1", Expiry: time.Now().Add(time.Hour)})
	row, err := f.db.ConnectorConnections().Get(ctx, f.conn.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range [][]byte{row.AccessSealed, row.RefreshSealed} {
		if bytes.Contains(b, []byte("sentinel")) {
			t.Fatal("a token is readable in the table")
		}
	}
	if row.Resource != f.conn.URL || row.ConnectedBy != "alice" || row.Status != store.ConnectionActive {
		t.Fatalf("row = %+v", row)
	}
	got, err := f.v.Token(ctx, f.conn, "alice")
	if err != nil || got != "access-sentinel-1" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	if f.oauth.refreshCount() != 0 {
		t.Fatal("a fresh token was refreshed")
	}
}

// A ciphertext copied to another person's row does not open: it is bound to
// the row it was sealed for.
func TestSwappedCiphertextDoesNotOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "alice-access", Expiry: time.Now().Add(time.Hour)})
	aliceRow, err := f.db.ConnectorConnections().Get(ctx, f.conn.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	// bob's row gets alice's sealed access token.
	if _, err := f.db.ConnectorConnections().Put(ctx, alice, store.ConnectorConnection{
		ConnectorID: f.conn.ID, Subject: "bob", ConnectedBy: "bob", Resource: f.conn.URL, ExpiresAt: time.Now().Add(time.Hour),
	}, func(string) ([]byte, []byte, error) { return aliceRow.AccessSealed, nil, nil }); err != nil {
		t.Fatal(err)
	}
	if got, err := f.v.Token(ctx, f.conn, "bob"); err == nil || got != "" {
		t.Fatalf("Token for bob with alice's ciphertext = %q, %v; want an error", got, err)
	}
}

func TestNoConnectionIsNotConnected(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.v.Token(ctx, f.conn, "bob"); !errors.Is(err, connectors.ErrNotConnected) {
		t.Fatalf("Token without a connection = %v, want ErrNotConnected", err)
	}
}

// A token is sent only to the address it was issued for: once the
// connector's URL is another, the connection is not used.
func TestTokenForAnotherAddressIsNotUsed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "a", Expiry: time.Now().Add(time.Hour)})
	moved := f.conn
	moved.URL = "https://elsewhere.example.org/mcp"
	if got, err := f.v.Token(ctx, moved, "alice"); !errors.Is(err, connectors.ErrNotConnected) || got != "" {
		t.Fatalf("Token for a moved connector = %q, %v; want ErrNotConnected", got, err)
	}
}

// An access token about to expire is refreshed; the rotated refresh token
// is the one used next time.
func TestRefreshOnExpiryStoresRotation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "old", Refresh: "refresh-0", Expiry: time.Now().Add(30 * time.Second)})
	got, err := f.v.Token(ctx, f.conn, "alice")
	if err != nil || got != "access-1" {
		t.Fatalf("Token = %q, %v", got, err)
	}
	// The new access token is good for an hour: no second refresh.
	if got, err := f.v.Token(ctx, f.conn, "alice"); err != nil || got != "access-1" || f.oauth.refreshCount() != 1 {
		t.Fatalf("second Token = %q, %v after %d refreshes", got, err, f.oauth.refreshCount())
	}
	// Invalidate (the connector refused access-1) refreshes with the
	// rotated refresh token.
	if err := f.v.Invalidate(ctx, f.conn, "alice", "access-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.v.Token(ctx, f.conn, "alice"); err != nil || got != "access-2" {
		t.Fatalf("Token after Invalidate = %q, %v", got, err)
	}
	if want := []string{"refresh-0", "refresh-1"}; strings.Join(f.oauth.refreshes, ",") != strings.Join(want, ",") {
		t.Fatalf("refreshed with %v, want %v", f.oauth.refreshes, want)
	}
	// Invalidating a token already replaced does not refresh again.
	if err := f.v.Invalidate(ctx, f.conn, "alice", "access-1"); err != nil || f.oauth.refreshCount() != 2 {
		t.Fatalf("Invalidate of a replaced token = %v after %d refreshes", err, f.oauth.refreshCount())
	}
}

// Without rotation the stored refresh token is kept.
func TestRefreshWithoutRotationKeepsRefreshToken(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.oauth.noRotate = true
	f.put(t, "alice", vault.Tokens{Access: "old", Refresh: "refresh-0", Expiry: time.Now().Add(-time.Minute)})
	if _, err := f.v.Token(ctx, f.conn, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := f.v.Invalidate(ctx, f.conn, "alice", "access-1"); err != nil {
		t.Fatal(err)
	}
	if want := "refresh-0,refresh-0"; strings.Join(f.oauth.refreshes, ",") != want {
		t.Fatalf("refreshed with %v, want %s", f.oauth.refreshes, want)
	}
}

// Two gateway replicas (two vaults over one database), each with several
// callers, refresh one expired connection once.
func TestConcurrentCallersRefreshOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "old", Refresh: "refresh-0", Expiry: time.Now().Add(-time.Minute)})
	f.oauth.gate = make(chan struct{})
	other := vault.New(f.cfg)
	var wg sync.WaitGroup
	got := make([]string, 6)
	errs := make([]error, 6)
	for i := range got {
		v := f.v
		if i%2 == 1 {
			v = other
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = v.Token(ctx, f.conn, "alice")
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(f.oauth.gate)
	wg.Wait()
	for i := range got {
		if errs[i] != nil || got[i] != "access-1" {
			t.Errorf("caller %d got %q, %v", i, got[i], errs[i])
		}
	}
	if n := f.oauth.refreshCount(); n != 1 {
		t.Fatalf("%d refreshes, want 1", n)
	}
}

// A refresh token the vendor refuses marks the connection needs_reauth and
// answers not connected, with an audit row; no token is in the logs.
func TestInvalidGrantNeedsReauth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	f.oauth.refuse = true
	f.put(t, "alice", vault.Tokens{Access: "access-sentinel", Refresh: "refresh-sentinel", Expiry: time.Now().Add(-time.Minute)})
	if _, err := f.v.Token(ctx, f.conn, "alice"); !errors.Is(err, connectors.ErrNotConnected) {
		t.Fatalf("Token after invalid_grant = %v, want ErrNotConnected", err)
	}
	row, err := f.db.ConnectorConnections().Get(ctx, f.conn.ID, "alice")
	if err != nil || row.Status != store.ConnectionNeedsReauth {
		t.Fatalf("row = %+v, %v", row, err)
	}
	// It is not tried again until the person connects again.
	if _, err := f.v.Token(ctx, f.conn, "alice"); !errors.Is(err, connectors.ErrNotConnected) || f.oauth.refreshCount() != 1 {
		t.Fatalf("second Token = %v after %d refreshes", err, f.oauth.refreshCount())
	}
	events, err := f.db.Audit().List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Action != "connector.refresh_failed" || events[0].ObjectName != "fake" || events[0].ObjectOwner != "alice" {
		t.Fatalf("latest audit row = %+v", events[0])
	}
	for _, e := range events {
		if strings.Contains(string(e.Detail), "sentinel") {
			t.Fatalf("audit detail %s holds a token", e.Detail)
		}
	}
	if strings.Contains(logs.String(), "sentinel") {
		t.Fatalf("logs hold a token: %s", logs.String())
	}
}

// Without a refresh token an expired connection needs connecting again.
func TestExpiredWithoutRefreshTokenNeedsReauth(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "a", Expiry: time.Now().Add(-time.Minute)})
	if _, err := f.v.Token(ctx, f.conn, "alice"); !errors.Is(err, connectors.ErrNotConnected) || f.oauth.refreshCount() != 0 {
		t.Fatalf("Token = %v after %d refreshes", err, f.oauth.refreshCount())
	}
}

// Disconnect revokes the refresh token, then the access token, and deletes
// the row; it deletes the row when revocation fails too, and says how
// revocation went on the audit log.
func TestDeleteRevokesThenDeletes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "a1", Refresh: "r1", Expiry: time.Now().Add(time.Hour)})
	if err := f.v.Delete(ctx, alice, f.conn, "alice"); err != nil {
		t.Fatal(err)
	}
	if want := "refresh_token:r1,access_token:a1"; strings.Join(f.oauth.revoked, ",") != want {
		t.Fatalf("revoked %v, want %s", f.oauth.revoked, want)
	}
	if _, err := f.v.Token(ctx, f.conn, "alice"); !errors.Is(err, connectors.ErrNotConnected) {
		t.Fatalf("Token after Delete = %v", err)
	}

	f.oauth.failRev = errors.New("revocation endpoint unavailable")
	f.put(t, "bob", vault.Tokens{Access: "a2", Expiry: time.Now().Add(time.Hour)})
	if err := f.v.Delete(ctx, alice, f.conn, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ConnectorConnections().Get(ctx, f.conn.ID, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob's row after a failed revocation = %v, want gone", err)
	}
	events, err := f.db.Audit().List(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Action != "connector.disconnect" || !strings.Contains(string(events[0].Detail), `"revoke":"failed"`) {
		t.Fatalf("latest audit row = %+v %s", events[0], events[0].Detail)
	}
	if err := f.v.Delete(ctx, alice, f.conn, "bob"); !errors.Is(err, connectors.ErrNotConnected) {
		t.Fatalf("Delete of no connection = %v, want ErrNotConnected", err)
	}
}

// Every connection to a connector goes, the company-wide one included.
func TestDeleteForConnector(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.put(t, "alice", vault.Tokens{Access: "a1", Expiry: time.Now().Add(time.Hour)})
	f.put(t, "", vault.Tokens{Access: "a2", Expiry: time.Now().Add(time.Hour)})
	if err := f.v.DeleteForConnector(ctx, alice, f.conn); err != nil {
		t.Fatal(err)
	}
	left, err := f.v.ForConnector(ctx, f.conn.ID)
	if err != nil || len(left) != 0 || len(f.oauth.revoked) != 2 {
		t.Fatalf("left %+v, %v; revoked %v", left, err, f.oauth.revoked)
	}
}

// Tokens never print, whatever verb or logger.
func TestTokensPrintRedacted(t *testing.T) {
	t.Parallel()
	tok := vault.Tokens{Access: "access-sentinel", Refresh: "refresh-sentinel"}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "tokens", tok)
	for _, s := range []string{fmt.Sprintf("%v %+v %#v %s %q", tok, tok, tok, tok, tok), logs.String()} {
		if strings.Contains(s, "sentinel") {
			t.Fatalf("printed %s", s)
		}
	}
}
