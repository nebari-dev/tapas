// Package catalog is the built-in catalogue of connectors: prefilled forms
// for well-known vendors' MCP servers. Enabling an entry copies it into a
// connector row; from then on only the row is read, so a later change here
// never changes a live connector.
//
// Each entry's address, endpoints, scopes and tool names were checked
// against the vendor's own documentation, linked in its docs field.
package catalog

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sync"

	"github.com/nebari-dev/javo/internal/connectors"
)

// Shipped is the catalogue this build carries.
//
//go:embed catalog.json
var Shipped []byte

// How an entry's OAuth endpoints are found (Entry.OAuthEndpoints).
const (
	// EndpointsDiscovered: from the MCP server's metadata (RFC 9728, RFC 8414).
	EndpointsDiscovered = "discovered"
	// EndpointsStatic: the entry's OAuth addresses.
	EndpointsStatic = "static"
)

// Entry is one catalogue entry.
type Entry struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	// URL is the vendor's hosted MCP server; empty when there is none to
	// name and the admin fills it in.
	URL            string                         `json:"url"`
	AuthMode       connectors.AuthMode            `json:"authMode"`
	Scopes         []string                       `json:"scopes"`
	ToolModes      map[string]connectors.ToolMode `json:"toolModes"`
	OAuthEndpoints string                         `json:"oauthEndpoints"`
	OAuth          Endpoints                      `json:"oauth"`
	// Setup tells the admin what to do at the vendor; Docs links the
	// vendor's page for its MCP server.
	Setup string `json:"setup"`
	Docs  string `json:"docs"`
}

// Endpoints are an entry's static OAuth addresses.
type Endpoints struct {
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	RevokeURL    string `json:"revokeUrl,omitempty"`
}

// Connector is the connector enabling e makes, still turned off and with
// no OAuth client, which the admin adds.
func (e Entry) Connector() connectors.Connector {
	return connectors.Connector{
		Slug: e.Slug, Name: e.Name, CatalogID: e.ID, URL: e.URL, AuthMode: e.AuthMode,
		OAuth:  connectors.OAuth{AuthorizeURL: e.OAuth.AuthorizeURL, TokenURL: e.OAuth.TokenURL, RevokeURL: e.OAuth.RevokeURL},
		Scopes: slices.Clone(e.Scopes), AllowedGroups: []string{}, ToolModes: maps.Clone(e.ToolModes),
	}
}

func (e Entry) clone() Entry {
	e.Scopes, e.ToolModes = slices.Clone(e.Scopes), maps.Clone(e.ToolModes)
	return e
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

// Load reads a catalogue and refuses it when an entry does not validate or
// an id or slug repeats.
func Load(b []byte) ([]Entry, error) {
	var doc struct {
		Entries []Entry `json:"entries"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	ids, slugs := map[string]bool{}, map[string]bool{}
	var errs []error
	for i, e := range doc.Entries {
		if err := check(e); err != nil {
			errs = append(errs, fmt.Errorf("catalog entry %d (%s): %w", i, e.ID, err))
		}
		if ids[e.ID] || slugs[e.Slug] {
			errs = append(errs, fmt.Errorf("catalog entry %d (%s): id or slug repeats", i, e.ID))
		}
		ids[e.ID], slugs[e.Slug] = true, true
	}
	if len(doc.Entries) == 0 {
		errs = append(errs, errors.New("catalog: no entries"))
	}
	return doc.Entries, errors.Join(errs...)
}

func check(e Entry) error {
	if !idPattern.MatchString(e.ID) {
		return errors.New("id is not lowercase letters, digits and dashes")
	}
	if e.Setup == "" || connectors.CheckURL(e.Docs, nil) != nil {
		return errors.New("setup text and an https docs link are required")
	}
	static := e.OAuth != Endpoints{}
	switch e.OAuthEndpoints {
	case EndpointsDiscovered:
		if static {
			return errors.New("discovered endpoints with static OAuth addresses")
		}
	case EndpointsStatic:
		if e.OAuth.AuthorizeURL == "" || e.OAuth.TokenURL == "" {
			return errors.New("static endpoints need authorizeUrl and tokenUrl")
		}
	default:
		return fmt.Errorf("oauthEndpoints %q is not discovered or static", e.OAuthEndpoints)
	}
	c := e.Connector()
	if c.URL == "" {
		// The admin fills the address in; everything else is checked as it
		// will be saved.
		c.URL = "https://mcp.example.org/mcp"
	}
	return c.Validate(nil)
}

var shipped = sync.OnceValue(func() []Entry {
	entries, err := Load(Shipped)
	if err != nil {
		// The catalogue is part of the build and its test loads it.
		panic(err)
	}
	return entries
})

// All returns a copy of the shipped catalogue's entries, in its order.
func All() []Entry {
	out := make([]Entry, 0, len(shipped()))
	for _, e := range shipped() {
		out = append(out, e.clone())
	}
	return out
}

// Get returns a copy of the shipped entry with id.
func Get(id string) (Entry, bool) {
	for _, e := range shipped() {
		if e.ID == id {
			return e.clone(), true
		}
	}
	return Entry{}, false
}
