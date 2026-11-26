package registry_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

var (
	ctx   = context.Background()
	alice = model.Actor{Person: "alice"}
)

const secret = "client-secret-sentinel-7f3a"

func newRegistry(t *testing.T) (*registry.Registry, *store.Store) {
	t.Helper()
	db := storetest.New(t)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return registry.New(registry.Config{DB: db, Seal: key, InternalHosts: []string{"mcp.tapas.svc"}}), db
}

func fakeInput() registry.Input {
	return registry.Input{
		Slug: "fake", Name: "Fake", URL: "https://mcp.example.org/mcp", AuthMode: connectors.AuthUser, Enabled: true,
		ClientID: "client-1", ClientSecret: secret, Scopes: []string{"read"}, AllowedGroups: []string{"sales"},
		ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}
}

func fieldOf(t *testing.T, err error) *connectors.FieldError {
	t.Helper()
	var fe *connectors.FieldError
	if !errors.As(err, &fe) {
		t.Fatalf("error %v is not a field error", err)
	}
	return fe
}

// The client secret is stored sealed, opens again for the connector it was
// saved for, and is in no view the registry returns.
func TestClientSecretRoundTripsSealedAndIsNeverInAView(t *testing.T) {
	t.Parallel()
	r, db := newRegistry(t)
	c, err := r.Create(ctx, alice, fakeInput())
	if err != nil {
		t.Fatal(err)
	}
	if !c.OAuth.SecretStored || c.OAuth.Registration != connectors.RegistrationStatic || c.OAuth.ClientID != "client-1" {
		t.Fatalf("Create = %+v", c)
	}
	views := []connectors.Connector{c}
	got, err := r.Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	views = append(append(views, got), listed...)
	for _, v := range views {
		if s := fmt.Sprintf("%+v %#v", v, v); strings.Contains(s, secret) {
			t.Fatalf("a view holds the secret: %s", s)
		}
	}
	row, err := db.Connectors().Get(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.OAuthClientSecretSealed) == 0 || bytes.Contains(row.OAuthClientSecretSealed, []byte(secret)) {
		t.Fatalf("stored secret %q is not sealed", row.OAuthClientSecretSealed)
	}
	if s, err := r.ClientSecret(ctx, c.ID); err != nil || s != secret {
		t.Fatalf("ClientSecret = %q, %v", s, err)
	}

	// A ciphertext copied onto another connector's row does not open there.
	other := fakeInput()
	other.Slug, other.ClientSecret = "other", "another-secret"
	o, err := r.Create(ctx, alice, other)
	if err != nil {
		t.Fatal(err)
	}
	orow, err := db.Connectors().Get(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	orow.OAuthClientSecretSealed = row.OAuthClientSecretSealed
	if _, err := db.Connectors().Update(ctx, alice, orow, nil); err != nil {
		t.Fatal(err)
	}
	if s, err := r.ClientSecret(ctx, o.ID); err == nil || s != "" {
		t.Fatalf("a secret moved to another row opened: %q, %v", s, err)
	}
}

// Changing where the connector is, its OAuth endpoints or its client needs
// the secret again: a stored secret is only sent where it was saved for.
func TestChangingTheAddressOrClientNeedsTheSecretAgain(t *testing.T) {
	t.Parallel()
	r, _ := newRegistry(t)
	c, err := r.Create(ctx, alice, fakeInput())
	if err != nil {
		t.Fatal(err)
	}
	update := func(change func(*registry.Input)) (connectors.Connector, error) {
		in := fakeInput()
		in.ClientSecret, in.Rev = "", c.Rev
		change(&in)
		return r.Update(ctx, alice, c.ID, in)
	}
	for name, change := range map[string]func(*registry.Input){
		"another host": func(in *registry.Input) { in.URL = "https://mcp.example.com/mcp" },
		"another port": func(in *registry.Input) { in.URL = "https://mcp.example.org:8443/mcp" },
		"static token endpoint": func(in *registry.Input) {
			in.AuthorizeURL, in.TokenURL = "https://idp.example.org/a", "https://idp.example.org/t"
		},
		"another client":          func(in *registry.Input) { in.ClientID = "client-2" },
		"secret without a client": func(in *registry.Input) { in.ClientID, in.ClientSecret = "", "x" },
	} {
		_, err := update(change)
		fe := fieldOf(t, err)
		if name == "secret without a client" {
			if fe.Field != "oauth.clientId" || fe.Code != reasons.MsgConnectorNeedsOAuthClient {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if fe.Field != "oauth.clientSecret" || fe.Code != reasons.MsgConnectorSecretForNewAddress {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, err := r.ClientSecret(ctx, c.ID); err != nil || s != secret {
		t.Fatalf("a refused change touched the secret: %q, %v", s, err)
	}

	// Another path on the same origin keeps the secret.
	c, err = update(func(in *registry.Input) { in.URL = "https://mcp.example.org/v2/mcp" })
	if err != nil || !c.OAuth.SecretStored {
		t.Fatalf("same origin: %+v, %v", c, err)
	}
	// A new address with the secret pasted again is saved.
	c, err = update(func(in *registry.Input) { in.URL, in.ClientSecret = "https://mcp.example.com/mcp", "new-secret" })
	if err != nil || c.URL != "https://mcp.example.com/mcp" {
		t.Fatalf("new address with a secret: %+v, %v", c, err)
	}
	if s, _ := r.ClientSecret(ctx, c.ID); s != "new-secret" {
		t.Fatalf("ClientSecret = %q", s)
	}
	// Removing the client removes the secret.
	c, err = update(func(in *registry.Input) { in.URL, in.ClientID = "https://mcp.example.com/mcp", "" })
	if err != nil || c.OAuth.SecretStored || c.OAuth.ClientID != "" || c.OAuth.Registration != "" {
		t.Fatalf("no client: %+v, %v", c, err)
	}
}

func TestUpdateRecordsTheChangedFieldsAndRefusesAStaleRev(t *testing.T) {
	t.Parallel()
	r, db := newRegistry(t)
	c, err := r.Create(ctx, alice, fakeInput())
	if err != nil {
		t.Fatal(err)
	}
	in := fakeInput()
	in.ClientSecret, in.Rev, in.Name, in.Enabled = "", c.Rev, "Renamed", false
	in.ToolModes = map[string]connectors.ToolMode{"echo": connectors.ToolOff}
	if _, err := r.Update(ctx, alice, c.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(ctx, alice, c.ID, in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Update with a stale rev = %v, want ErrConflict", err)
	}
	if _, err := r.Update(ctx, alice, "c-missing", in); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update of a missing connector = %v, want ErrNotFound", err)
	}
	events, err := db.Audit().List(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(events[0].Detail); got != `{"fields":["name","enabled","toolModes"]}` {
		t.Fatalf("audit detail = %s", got)
	}
}

func TestCreateFromACatalogueEntryFillsWhatIsNotGiven(t *testing.T) {
	t.Parallel()
	r, _ := newRegistry(t)
	c, err := r.Create(ctx, alice, registry.Input{CatalogID: "google-drive", ClientID: "client-1", ClientSecret: secret, AllowedGroups: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Slug != "gdrive" || c.CatalogID != "google-drive" || !strings.HasPrefix(c.URL, "https://") || c.OAuth.TokenURL == "" ||
		len(c.Scopes) == 0 || len(c.ToolModes) == 0 || c.AuthMode != connectors.AuthUser || c.Enabled {
		t.Fatalf("Create from the catalogue = %+v", c)
	}
	// What the admin gives wins over the entry.
	c, err = r.Create(ctx, alice, registry.Input{CatalogID: "notion", Slug: "notes", Name: "Team notes", Scopes: []string{"read"}})
	if err != nil || c.Slug != "notes" || c.Name != "Team notes" || !slices.Equal(c.Scopes, []string{"read"}) || c.URL == "" {
		t.Fatalf("Create with overrides = %+v, %v", c, err)
	}
	if _, err := r.Create(ctx, alice, registry.Input{CatalogID: "nope"}); fieldOf(t, err).Field != "catalogId" {
		t.Fatalf("unknown entry: %v", err)
	}
}

func TestCreateRefusesABadAddressAndATakenSlug(t *testing.T) {
	t.Parallel()
	r, _ := newRegistry(t)
	in := fakeInput()
	in.URL = "http://mcp.example.org/mcp"
	if fe := fieldOf(t, func() error { _, err := r.Create(ctx, alice, in); return err }()); fe.Field != "url" {
		t.Fatalf("http address: %v", fe)
	}
	// Plain http is fine for a host in connectors.internalHosts.
	in.URL = "http://mcp.tapas.svc:8080/mcp"
	if _, err := r.Create(ctx, alice, in); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, alice, in); !errors.Is(err, store.ErrExists) {
		t.Fatalf("taken slug = %v, want ErrExists", err)
	}
}

func TestDeleteRemovesTheConnector(t *testing.T) {
	t.Parallel()
	r, _ := newRegistry(t)
	c, err := r.Create(ctx, alice, fakeInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, alice, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	if err := r.Delete(ctx, alice, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}

// forgetter records which connectors' connections were dropped, and with
// which address: the one the tokens were issued for. then, when set, runs
// after each.
type forgetter struct {
	forgot []string
	then   func(c connectors.Connector)
}

func (f *forgetter) DeleteForConnector(_ context.Context, _ model.Actor, c connectors.Connector) error {
	f.forgot = append(f.forgot, c.Slug+"@"+c.URL)
	if f.then != nil {
		f.then(c)
	}
	return nil
}

func newRegistryWith(t *testing.T, f *forgetter) *registry.Registry {
	t.Helper()
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	return registry.New(registry.Config{DB: storetest.New(t), Seal: key, Connections: f})
}

// A new address or client drops what people connected, for the address
// they connected to; other changes keep it. Deleting drops it too.
func TestAddressChangeAndDeleteForgetConnections(t *testing.T) {
	t.Parallel()
	f := &forgetter{}
	r := newRegistryWith(t, f)
	in := fakeInput()
	c, err := r.Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Rev, in.Name = c.Rev, "Renamed"
	if c, err = r.Update(ctx, alice, c.ID, in); err != nil || len(f.forgot) != 0 {
		t.Fatalf("rename: %v, forgot %v", err, f.forgot)
	}
	in.Rev, in.URL = c.Rev, "https://other.example.org/mcp"
	if c, err = r.Update(ctx, alice, c.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, alice, c.ID); err != nil {
		t.Fatal(err)
	}
	want := "fake@https://mcp.example.org/mcp fake@https://other.example.org/mcp"
	if strings.Join(f.forgot, " ") != want {
		t.Fatalf("forgot %v, want %s", f.forgot, want)
	}
}

// A sign-in that finishes while an admin moves the connector (its tokens
// stored after the old connections were revoked, before the move is
// saved) leaves no connection behind: the move removes it.
func TestAMoveRemovesAConnectionStoredWhileItWasSaved(t *testing.T) {
	t.Parallel()
	db := storetest.New(t)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	f := &forgetter{}
	r := registry.New(registry.Config{DB: db, Seal: key, Connections: f})
	in := fakeInput()
	c, err := r.Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	f.then = func(c connectors.Connector) {
		if _, err := db.ConnectorConnections().Put(ctx, alice, store.ConnectorConnection{
			ConnectorID: c.ID, Subject: "bob", ConnectedBy: "bob", Resource: c.URL,
		}, c.Rev, func(string) ([]byte, []byte, error) { return []byte("sealed"), nil, nil }); err != nil {
			t.Error(err)
		}
	}
	in.Rev, in.URL = c.Rev, "https://other.example.org/mcp"
	if _, err := r.Update(ctx, alice, c.ID, in); err != nil {
		t.Fatal(err)
	}
	if left, err := db.ConnectorConnections().ForConnector(ctx, c.ID); err != nil || len(left) != 0 {
		t.Fatalf("connections after the move: %+v, %v", left, err)
	}
}

// A client Tapas registered itself is stored on the connector, its secret
// sealed, marked dynamic; a stale rev is refused.
func TestRegisteredStoresADynamicClient(t *testing.T) {
	t.Parallel()
	r, _ := newRegistry(t)
	in := fakeInput()
	in.ClientID, in.ClientSecret = "", ""
	c, err := r.Create(ctx, alice, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Registered(ctx, alice, c, "dyn-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuth.ClientID != "dyn-1" || got.OAuth.Registration != connectors.RegistrationDynamic || !got.OAuth.SecretStored || got.Rev == c.Rev {
		t.Fatalf("Registered = %+v", got.OAuth)
	}
	if s, err := r.ClientSecret(ctx, c.ID); err != nil || s != secret {
		t.Fatalf("ClientSecret = %q, %v", s, err)
	}
	if _, err := r.Registered(ctx, alice, c, "dyn-2", secret); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Registered on a stale rev = %v, want ErrConflict", err)
	}
}

func TestSetToolsReplacesTheSnapshot(t *testing.T) {
	t.Parallel()
	r, _ := newRegistry(t)
	c, err := r.Create(ctx, alice, fakeInput())
	if err != nil {
		t.Fatal(err)
	}
	tools := []connectors.Tool{{Name: "echo", Description: "Echo", ReadOnlyHint: true}, {Name: "post"}}
	got, err := r.SetTools(ctx, alice, c.ID, tools)
	if err != nil || !slices.Equal(got.Tools, tools) || got.Rev == c.Rev {
		t.Fatalf("SetTools = %+v, %v", got, err)
	}
}
