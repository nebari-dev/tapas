package store_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// sealedTokens stands in for the vault: each "ciphertext" names the row it
// was sealed for.
func sealedTokens(access, refresh string) func(id string) ([]byte, []byte, error) {
	return func(id string) ([]byte, []byte, error) {
		var r []byte
		if refresh != "" {
			r = []byte("sealed:" + id + ":" + refresh)
		}
		return []byte("sealed:" + id + ":" + access), r, nil
	}
}

func createConnector(t *testing.T, s *store.Store, slug string) store.Connector {
	t.Helper()
	c, err := s.Connectors().Create(ctx, alice, newConnector(slug), sealedAs("secret"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConnectorConnectionsPutGetListDelete(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	expires := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	in := store.ConnectorConnection{
		ConnectorID: c.ID, Subject: "alice", ConnectedBy: "alice", Resource: c.URL,
		Scopes: []string{"read"}, ExpiresAt: expires,
	}
	got, err := s.ConnectorConnections().Put(ctx, alice, in, c.Rev, sealedTokens("a1", "r1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.Rev == "" || got.Status != store.ConnectionActive || string(got.AccessSealed) != "sealed:"+got.ID+":a1" ||
		string(got.RefreshSealed) != "sealed:"+got.ID+":r1" || !got.ExpiresAt.Equal(expires) || got.CreatedAt.IsZero() {
		t.Fatalf("Put = %+v", got)
	}
	read, err := s.ConnectorConnections().Get(ctx, c.ID, "alice")
	if err != nil || read.ID != got.ID || read.Resource != c.URL || len(read.Scopes) != 1 {
		t.Fatalf("Get = %+v, %v", read, err)
	}
	if _, err := s.ConnectorConnections().Get(ctx, c.ID, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get bob = %v, want ErrNotFound", err)
	}

	// Connecting again replaces the row, under a new id the ciphertexts are
	// bound to. No expiry is stored as none.
	in.ExpiresAt = time.Time{}
	again, err := s.ConnectorConnections().Put(ctx, alice, in, c.Rev, sealedTokens("a2", ""))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == got.ID || string(again.AccessSealed) != "sealed:"+again.ID+":a2" || again.RefreshSealed != nil || !again.ExpiresAt.IsZero() {
		t.Fatalf("second Put = %+v", again)
	}
	other := createConnector(t, s, "other")
	if _, err := s.ConnectorConnections().Put(ctx, alice, store.ConnectorConnection{
		ConnectorID: other.ID, Subject: "", ConnectedBy: "alice", Resource: other.URL,
	}, other.Rev, sealedTokens("shared", "")); err != nil {
		t.Fatal(err)
	}
	mine, err := s.ConnectorConnections().ForSubject(ctx, "alice")
	if err != nil || len(mine) != 1 || mine[0].ConnectorID != c.ID {
		t.Fatalf("ForSubject = %+v, %v", mine, err)
	}
	all, err := s.ConnectorConnections().ForConnector(ctx, other.ID)
	if err != nil || len(all) != 1 || all[0].Subject != "" {
		t.Fatalf("ForConnector = %+v, %v", all, err)
	}

	if err := s.ConnectorConnections().Delete(ctx, alice, c.ID, "alice", map[string]string{"revoke": "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ConnectorConnections().Delete(ctx, alice, c.ID, "alice", nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	// Deleting the connector deletes its connections.
	if err := s.Connectors().Delete(ctx, alice, other.ID); err != nil {
		t.Fatal(err)
	}
	if left, err := s.ConnectorConnections().ForConnector(ctx, other.ID); err != nil || len(left) != 0 {
		t.Fatalf("ForConnector after the connector went = %+v, %v", left, err)
	}
}

// A connection is stored only for the connector revision its sign-in ran
// on: tokens from a code exchange that raced an admin's change are not
// kept under the new configuration.
func TestConnectorConnectionPutNeedsTheConnectorRevision(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	changed := c
	changed.URL = "https://mcp.example.org/moved"
	changed, err := s.Connectors().Update(ctx, alice, changed, []string{"url"})
	if err != nil {
		t.Fatal(err)
	}
	in := store.ConnectorConnection{ConnectorID: c.ID, Subject: "alice", ConnectedBy: "alice", Resource: c.URL}
	if _, err := s.ConnectorConnections().Put(ctx, alice, in, c.Rev, sealedTokens("a1", "")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Put on the old revision = %v, want ErrConflict", err)
	}
	if _, err := s.ConnectorConnections().Get(ctx, c.ID, "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a connection was stored: %v", err)
	}
	in.Resource = changed.URL
	if _, err := s.ConnectorConnections().Put(ctx, alice, in, changed.Rev, sealedTokens("a1", "")); err != nil {
		t.Fatal(err)
	}
}

// A refresh claim is won by one caller at a time, for the rev it read; the
// winner's new tokens are guarded by that rev too.
func TestConnectorConnectionRefreshClaimAndTokens(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	conns := s.ConnectorConnections()
	row, err := conns.Put(ctx, alice, store.ConnectorConnection{ConnectorID: c.ID, Subject: "alice", ConnectedBy: "alice", Resource: c.URL}, c.Rev, sealedTokens("a1", "r1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	won, err := conns.ClaimRefresh(ctx, row.ID, row.Rev, now, now.Add(30*time.Second))
	if err != nil || !won {
		t.Fatalf("first claim = %v, %v", won, err)
	}
	if won, err := conns.ClaimRefresh(ctx, row.ID, row.Rev, now, now.Add(30*time.Second)); err != nil || won {
		t.Fatalf("second claim while the first holds = %v, %v", won, err)
	}
	// Once the claim has run out, another caller wins it.
	later := now.Add(31 * time.Second)
	if won, err := conns.ClaimRefresh(ctx, row.ID, row.Rev, later, later.Add(30*time.Second)); err != nil || !won {
		t.Fatalf("claim after the first ran out = %v, %v", won, err)
	}

	expires := now.Add(time.Hour).Truncate(time.Millisecond)
	next, err := conns.SetTokens(ctx, row.ID, row.Rev, []byte("a2"), []byte("r2"), expires)
	if err != nil || next.Rev == row.Rev || string(next.AccessSealed) != "a2" || !next.ExpiresAt.Equal(expires) || next.Status != store.ConnectionActive {
		t.Fatalf("SetTokens = %+v, %v", next, err)
	}
	if _, err := conns.SetTokens(ctx, row.ID, row.Rev, []byte("a3"), nil, time.Time{}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("SetTokens on a stale rev = %v, want ErrConflict", err)
	}
	// A claim on the old rev fails: the row moved on.
	if won, err := conns.ClaimRefresh(ctx, row.ID, row.Rev, later, later.Add(time.Minute)); err != nil || won {
		t.Fatalf("claim on a stale rev = %v, %v", won, err)
	}

	if err := conns.SetStatus(ctx, row.ID, store.ConnectionNeedsReauth); err != nil {
		t.Fatal(err)
	}
	got, err := conns.Get(ctx, c.ID, "alice")
	if err != nil || got.Status != store.ConnectionNeedsReauth {
		t.Fatalf("after SetStatus = %+v, %v", got, err)
	}
}

func TestConnectorConnectionsDeleteForSubjectSparesShared(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	shared := createConnector(t, s, "shared")
	conns := s.ConnectorConnections()
	for _, in := range []store.ConnectorConnection{
		{ConnectorID: c.ID, Subject: "alice", ConnectedBy: "alice", Resource: c.URL},
		{ConnectorID: c.ID, Subject: "bob", ConnectedBy: "bob", Resource: c.URL},
		{ConnectorID: shared.ID, Subject: "", ConnectedBy: "alice", Resource: shared.URL},
	} {
		rev := map[string]string{c.ID: c.Rev, shared.ID: shared.Rev}[in.ConnectorID]
		if _, err := conns.Put(ctx, alice, in, rev, sealedTokens("a", "")); err != nil {
			t.Fatal(err)
		}
	}
	n, err := conns.DeleteForSubject(ctx, model.Actor{System: model.SystemCLI}, "alice")
	if err != nil || n != 1 {
		t.Fatalf("DeleteForSubject = %d, %v", n, err)
	}
	if n, err := conns.DeleteForSubject(ctx, alice, ""); err != nil || n != 0 {
		t.Fatalf("DeleteForSubject of the shared subject = %d, %v; want nothing deleted", n, err)
	}
	if _, err := conns.Get(ctx, c.ID, "bob"); err != nil {
		t.Fatalf("bob's connection: %v", err)
	}
	if _, err := conns.Get(ctx, shared.ID, ""); err != nil {
		t.Fatalf("the shared connection: %v", err)
	}
}

// Connect and disconnect are audited with names only: never a token.
func TestConnectorConnectionWritesAreAuditedWithoutTokens(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	conns := s.ConnectorConnections()
	row, err := conns.Put(ctx, alice, store.ConnectorConnection{ConnectorID: c.ID, Subject: "alice", ConnectedBy: "alice", Resource: c.URL}, c.Rev, sealedTokens("SENTINEL-ACCESS", "SENTINEL-REFRESH"))
	if err != nil {
		t.Fatal(err)
	}
	// Plumbing is not audited.
	if _, err := conns.ClaimRefresh(ctx, row.ID, row.Rev, time.Now(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := conns.SetTokens(ctx, row.ID, row.Rev, []byte("SENTINEL-ACCESS"), nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := conns.Delete(ctx, alice, c.ID, "alice", map[string]string{"revoke": "failed"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.Audit().List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if strings.Contains(string(e.Detail), "SENTINEL") {
			t.Errorf("%s detail %s holds a token", e.Action, e.Detail)
		}
		if strings.HasPrefix(e.Action, "connector.connect") || strings.HasPrefix(e.Action, "connector.disconnect") {
			if e.ObjectKind != "connector" || e.ObjectName != "fake" || e.ObjectID != c.ID || e.ObjectOwner != "alice" {
				t.Errorf("event %+v", e)
			}
		}
		got = append(got, e.Action)
	}
	if want := "connector.disconnect connector.connect connector.create"; strings.Join(got, " ") != want {
		t.Fatalf("audit = %v, want %s", got, want)
	}
	var d map[string]string
	if err := json.Unmarshal(events[0].Detail, &d); err != nil || d["revoke"] != "failed" {
		t.Fatalf("disconnect detail = %s, %v", events[0].Detail, err)
	}
}

// An OAuth state is taken once; expired ones are swept.
func TestConnectorOAuthStatesAreSingleUse(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c := createConnector(t, s, "fake")
	conns := s.ConnectorConnections()
	now := time.Now().Truncate(time.Millisecond)
	st := store.ConnectorOAuthState{
		StateHash: []byte("hash-1"), ConnectorID: c.ID, ConnectorRev: c.Rev, Person: "alice", Subject: "alice",
		VerifierSealed: []byte("sealed"), RedirectURI: "https://tapas.example.org/settings/connections/callback",
		Issuer: "https://as.example.org", CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := conns.StartOAuth(ctx, st); err != nil {
		t.Fatal(err)
	}
	old := st
	old.StateHash, old.ExpiresAt = []byte("hash-2"), now.Add(-time.Minute)
	if err := conns.StartOAuth(ctx, old); err != nil {
		t.Fatal(err)
	}
	got, err := conns.TakeOAuthState(ctx, []byte("hash-1"))
	if err != nil || !bytes.Equal(got.StateHash, st.StateHash) || got.Person != "alice" || got.ConnectorRev != c.Rev ||
		got.RedirectURI != st.RedirectURI || got.Issuer != st.Issuer || !got.ExpiresAt.Equal(st.ExpiresAt) {
		t.Fatalf("TakeOAuthState = %+v, %v", got, err)
	}
	if _, err := conns.TakeOAuthState(ctx, []byte("hash-1")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second TakeOAuthState = %v, want ErrNotFound", err)
	}
	n, err := conns.SweepOAuthStates(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("SweepOAuthStates = %d, %v", n, err)
	}
	if _, err := conns.TakeOAuthState(ctx, []byte("hash-2")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("TakeOAuthState of a swept state = %v", err)
	}
}
