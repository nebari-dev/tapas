package store_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

func newConnector(slug string) store.Connector {
	return store.Connector{
		Slug: slug, Name: "Fake " + slug, CatalogID: "", URL: "https://mcp.example.org/" + slug, AuthMode: "user",
		Enabled: true, OAuthRegistration: "static", OAuthClientID: "client-1",
		Scopes: []string{"read", "write"}, AllowedGroups: []string{"sales"},
		ToolModes: map[string]string{"echo": "read", "post": "write"},
	}
}

func TestConnectorsCreateGetListUpdateDelete(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	c, err := s.Connectors().Create(ctx, alice, newConnector("fake"), sealedAs("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if c.ID == "" || string(c.OAuthClientSecretSealed) != "sealed:"+c.ID+":secret" || c.Rev == "" || c.CreatedAt.IsZero() {
		t.Fatalf("Create = %+v", c)
	}
	if _, err := s.Connectors().Create(ctx, alice, newConnector("fake"), sealedAs("secret")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("second Create with the same slug = %v, want ErrExists", err)
	}
	off := newConnector("other")
	off.Enabled = false
	if _, err := s.Connectors().Create(ctx, alice, off, func(string) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}

	got, err := s.Connectors().BySlug(ctx, "fake")
	if err != nil || got.ID != c.ID || got.URL != "https://mcp.example.org/fake" || !slices.Equal(got.Scopes, []string{"read", "write"}) ||
		got.ToolModes["post"] != "write" || !slices.Equal(got.AllowedGroups, []string{"sales"}) || got.Tools == nil {
		t.Fatalf("BySlug = %+v, %v", got, err)
	}
	if _, err := s.Connectors().Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get unknown = %v", err)
	}
	all, err := s.Connectors().List(ctx)
	if err != nil || len(all) != 2 || all[0].Slug != "fake" || all[1].Slug != "other" || all[1].OAuthClientSecretSealed != nil {
		t.Fatalf("List = %+v, %v", all, err)
	}
	enabled, err := s.Connectors().ListEnabled(ctx)
	if err != nil || len(enabled) != 1 || enabled[0].Slug != "fake" {
		t.Fatalf("ListEnabled = %+v, %v", enabled, err)
	}

	next := got
	next.Name, next.Enabled = "Renamed", false
	updated, err := s.Connectors().Update(ctx, alice, next, []string{"name", "enabled"})
	if err != nil || updated.Name != "Renamed" || updated.Enabled || updated.Rev == got.Rev {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	// got still carries the old rev: someone else saved first.
	if _, err := s.Connectors().Update(ctx, alice, got, []string{"name"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Update with a stale rev = %v, want ErrConflict", err)
	}
	missing := got
	missing.ID = "c-missing"
	if _, err := s.Connectors().Update(ctx, alice, missing, []string{"name"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Update of a missing connector = %v, want ErrNotFound", err)
	}

	tools := []store.ConnectorTool{{Name: "echo", Description: "Echo", ReadOnlyHint: true}, {Name: "post"}}
	withTools, err := s.Connectors().SetTools(ctx, alice, c.ID, tools)
	if err != nil || !slices.Equal(withTools.Tools, tools) || withTools.Rev == updated.Rev {
		t.Fatalf("SetTools = %+v, %v", withTools, err)
	}

	if err := s.Connectors().Delete(ctx, alice, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Connectors().Delete(ctx, alice, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	if _, err := s.Connectors().Get(ctx, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
}

// Each write leaves one audit row naming the connector, with the names of
// the fields that changed and never a value.
func TestConnectorWritesAreAuditedWithFieldNamesOnly(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	in := newConnector("fake")
	c, err := s.Connectors().Create(ctx, alice, in, sealedAs("secret"))
	if err != nil {
		t.Fatal(err)
	}
	c.URL = "https://mcp.example.org/moved"
	c, err = s.Connectors().Update(ctx, alice, c, []string{"url", "oauth.clientSecret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Connectors().Update(ctx, alice, c, []string{"name"}); err != nil {
		t.Fatal(err)
	}
	// A conflicting write is not recorded.
	if _, err := s.Connectors().Update(ctx, alice, c, []string{"name"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
	if err := s.Connectors().Delete(ctx, alice, c.ID); err != nil {
		t.Fatal(err)
	}
	events, err := s.Audit().List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if e.ObjectKind != "connector" || e.ObjectName != "fake" || e.ObjectID != c.ID || e.Actor != "alice" {
			t.Fatalf("event %+v", e)
		}
		for _, value := range []string{"mcp.example.org", "client-1", "sealed", "sales"} {
			if strings.Contains(string(e.Detail), value) {
				t.Errorf("%s detail %s holds the value %q", e.Action, e.Detail, value)
			}
		}
		var d struct{ Fields []string }
		if err := json.Unmarshal(e.Detail, &d); err != nil {
			t.Fatal(err)
		}
		got = append(got, e.Action+":"+strings.Join(d.Fields, ","))
	}
	want := []string{"connector.delete:", "connector.update:name", "connector.update:url,oauth.clientSecret", "connector.create:"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("audit = %v, want %v", got, want)
	}
}
