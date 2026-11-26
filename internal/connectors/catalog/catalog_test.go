package catalog_test

import (
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/catalog"
)

// The shipped catalogue loads: every entry validates and ids and slugs are
// unique (Load refuses otherwise, as the cases below show).
func TestTheShippedCatalogueIsValid(t *testing.T) {
	entries, err := catalog.Load(catalog.Shipped)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"slack", "github", "notion", "hubspot", "google-gmail", "google-calendar", "google-drive"}
	for _, id := range want {
		e, ok := catalog.Get(id)
		if !ok {
			t.Errorf("no entry %s", id)
			continue
		}
		if e.Setup == "" || e.Docs == "" {
			t.Errorf("%s has no setup text or docs link", id)
		}
	}
	if len(entries) != len(catalog.All()) {
		t.Errorf("All has %d entries, Load %d", len(catalog.All()), len(entries))
	}
}

func TestAllReturnsACopy(t *testing.T) {
	all := catalog.All()
	all[0].Scopes = append(all[0].Scopes, "everything")
	all[0].Name = "changed"
	if e, _ := catalog.Get(all[0].ID); e.Name == "changed" || strings.Contains(strings.Join(e.Scopes, " "), "everything") {
		t.Fatalf("the catalogue changed through All: %+v", e)
	}
}

func TestLoadRefusesABadEntry(t *testing.T) {
	entry := func(fields string) string {
		base := map[string]string{
			"id": `"fake"`, "slug": `"fake"`, "name": `"Fake"`, "url": `"https://mcp.example.org/mcp"`, "authMode": `"user"`,
			"oauthEndpoints": `"discovered"`, "setup": `"Paste a client."`, "docs": `"https://docs.example.org/mcp"`,
		}
		for _, kv := range strings.Split(fields, ";") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				base[k] = v
			}
		}
		var parts []string
		for k, v := range base {
			if v != "-" {
				parts = append(parts, `"`+k+`":`+v)
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	if _, err := catalog.Load([]byte(`{"entries":[` + entry("") + `]}`)); err != nil {
		t.Fatalf("the base entry is refused: %v", err)
	}
	for name, doc := range map[string]string{
		"duplicate id":          entry("") + "," + entry(`slug="other"`),
		"duplicate slug":        entry("") + "," + entry(`id="other"`),
		"bad id":                entry(`id="Fake_1"`),
		"bad slug":              entry(`slug="google-gmail"`),
		"http url":              entry(`url="http://mcp.example.org/mcp"`),
		"bad mode":              entry(`toolModes={"echo":"maybe"}`),
		"bad auth mode":         entry(`authMode="anyone"`),
		"no setup":              entry(`setup=-`),
		"no docs":               entry(`docs=-`),
		"static without urls":   entry(`oauthEndpoints="static"`),
		"discovered with urls":  entry(`oauth={"authorizeUrl":"https://idp.example.org/a","tokenUrl":"https://idp.example.org/t"}`),
		"unknown endpoint kind": entry(`oauthEndpoints="guessed"`),
		"unknown field":         entry(`colour="red"`),
	} {
		if _, err := catalog.Load([]byte(`{"entries":[` + doc + `]}`)); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
	// An empty address is allowed: the admin fills it in.
	if _, err := catalog.Load([]byte(`{"entries":[` + entry(`url=""`) + `]}`)); err != nil {
		t.Errorf("empty url: %v", err)
	}
}

// An entry copies into a connector that validates once it has an address.
func TestAnEntryBecomesAValidConnector(t *testing.T) {
	for _, e := range catalog.All() {
		c := e.Connector()
		if c.URL == "" {
			c.URL = "https://mcp.example.org/mcp"
		}
		if err := c.Validate(nil); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
		if c.CatalogID != e.ID || c.Enabled || c.AuthMode != connectors.AuthUser && c.AuthMode != connectors.AuthShared {
			t.Errorf("%s: %+v", e.ID, c)
		}
	}
}
