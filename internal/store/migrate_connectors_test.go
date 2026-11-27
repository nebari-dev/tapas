package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// The upgrade that ends the old mode where an admin connected one account
// for everyone: such a connector's one connection (and any sign-in for it
// in progress) goes, the connector is turned off for an admin to review,
// and every person's own connections stay.
func TestUpgradeEndsSharedConnectors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.Open(t, storetest.Config(t))
	if err := s.MigrateTo(ctx, 30); err != nil {
		t.Fatal(err)
	}
	connector := func(id, slug, mode string) {
		storetest.Exec(t, s, `INSERT INTO connectors (id, slug, name, catalog_id, url, auth_mode, enabled, oauth_registration,
			oauth_client_id, oauth_authorize_url, oauth_token_url, oauth_revoke_url, scopes, allowed_groups, tool_modes, tools,
			rev, created_at, updated_at) VALUES (?, ?, ?, '', ?, ?, TRUE, 'static', 'client-1', '', '', '', '[]', '["*"]', '{}', '[]', 'r-1', 0, 0)`,
			id, slug, slug, "https://mcp.example.org/"+slug, mode)
	}
	connection := func(id, connectorID, subject string) {
		storetest.Exec(t, s, `INSERT INTO connector_connections (id, connector_id, subject, connected_by, resource, access_sealed,
			scopes, status, refreshing_until, rev, created_at, updated_at) VALUES (?, ?, ?, 'alice', 'https://mcp.example.org/x', ?, '[]', 'active', 0, 'r-1', 0, 0)`,
			id, connectorID, subject, []byte("sealed"))
	}
	signIn := func(hash, connectorID, subject string) {
		storetest.Exec(t, s, `INSERT INTO connector_oauth_states (state_hash, connector_id, connector_rev, person, subject,
			verifier_sealed, redirect_uri, issuer, created_at, expires_at) VALUES (?, ?, 'r-1', 'alice', ?, ?, 'https://tapas.example.org/cb', '', 0, ?)`,
			[]byte(hash), connectorID, subject, []byte("sealed"), int64(1)<<50)
	}
	connector("c-1", "wiki", "shared")
	connector("c-2", "chat", "user")
	connection("k-1", "c-1", "")
	connection("k-2", "c-2", "alice")
	connection("k-3", "c-2", "")
	signIn("shared-sign-in", "c-1", "")
	signIn("own-sign-in", "c-2", "alice")
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	wiki, err := s.Connectors().Get(ctx, "c-1")
	if err != nil || wiki.Enabled {
		t.Fatalf("the shared connector = %+v, %v; want it turned off", wiki, err)
	}
	chat, err := s.Connectors().Get(ctx, "c-2")
	if err != nil || !chat.Enabled {
		t.Fatalf("the per-person connector = %+v, %v; want it unchanged", chat, err)
	}
	var left []string
	rows, err := s.DB().QueryContext(ctx, `SELECT id FROM connector_connections ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0] != "k-2" {
		t.Fatalf("connections left = %v, want only alice's own (k-2)", left)
	}
	if _, err := s.ConnectorConnections().TakeOAuthState(ctx, []byte("shared-sign-in")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a sign-in for the shared connection = %v, want gone", err)
	}
	if st, err := s.ConnectorConnections().TakeOAuthState(ctx, []byte("own-sign-in")); err != nil || st.Person != "alice" {
		t.Fatalf("alice's own sign-in = %+v, %v; want kept", st, err)
	}
	// A connector is written without the mode the schema no longer has.
	if _, err := s.Connectors().Create(ctx, alice, newConnector("after"), sealedAs("secret")); err != nil {
		t.Fatal(err)
	}
}

// A connection with no person is never stored, and a stray one is never
// read: every connection is one person's own.
func TestConnectorConnectionsHaveAPerson(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	conns := s.ConnectorConnections()
	if _, err := conns.Put(ctx, alice, store.ConnectorConnection{ConnectorID: c.ID, Subject: "", ConnectedBy: "alice", Resource: c.URL},
		c.Rev, sealedTokens("a", "")); err == nil {
		t.Fatal("Put with no person stored a connection")
	}
	storetest.Exec(t, s, `INSERT INTO connector_connections (id, connector_id, subject, connected_by, resource, access_sealed,
		scopes, status, refreshing_until, rev, created_at, updated_at) VALUES ('k-1', ?, '', 'alice', ?, ?, '[]', 'active', 0, 'r-1', 0, 0)`,
		c.ID, c.URL, []byte("sealed"))
	if _, err := conns.Get(ctx, c.ID, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get of a stray connection with no person = %v, want ErrNotFound", err)
	}
	if all, err := conns.ForConnector(ctx, c.ID); err != nil || len(all) != 0 {
		t.Fatalf("ForConnector = %+v, %v; want the stray row left out", all, err)
	}
	if all, err := conns.ForSubject(ctx, ""); err != nil || len(all) != 0 {
		t.Fatalf("ForSubject(\"\") = %+v, %v; want nothing", all, err)
	}
}
