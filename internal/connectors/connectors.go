// Package connectors is what every part of Tapas's connectors shares: the
// configuration of one connector (an MCP server an admin added), its
// validation, and how a connector's tools are named on Tapas's own MCP
// endpoint. It reaches no network and imports no MCP library.
package connectors

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nebari-dev/javo/internal/reasons"
)

// AuthMode is whose vendor account a connector's calls use.
type AuthMode string

const (
	// AuthUser: each person connects their own account.
	AuthUser AuthMode = "user"
	// AuthShared: an admin connects one account that every allowed person's
	// calls use.
	AuthShared AuthMode = "shared"
)

// ToolMode is how a connector's tool is treated: a read runs at once, a
// write becomes a plan its owner approves, an off tool is neither listed
// nor callable.
type ToolMode string

const (
	ToolRead  ToolMode = "read"
	ToolWrite ToolMode = "write"
	ToolOff   ToolMode = "off"
)

// ModeOf is the mode of tool: its entry in modes, and write for a tool
// that has none or whose entry is not a known mode.
func ModeOf(modes map[string]ToolMode, tool string) ToolMode {
	switch m := modes[tool]; m {
	case ToolRead, ToolOff:
		return m
	}
	return ToolWrite
}

// Registration says where a connector's OAuth client came from.
const (
	// RegistrationStatic: an admin pasted the client id (and secret).
	RegistrationStatic = "static"
	// RegistrationDynamic: Tapas registered the client (RFC 7591).
	RegistrationDynamic = "dynamic"
)

// Connector is one connector as an admin configured it. It never holds the
// OAuth client secret: OAuth.SecretStored says whether one is stored.
type Connector struct {
	ID, Slug, Name, CatalogID, URL string
	AuthMode                       AuthMode
	Enabled                        bool
	OAuth                          OAuth
	Scopes, AllowedGroups          []string
	ToolModes                      map[string]ToolMode
	Tools                          []Tool
	Rev                            string
	CreatedAt, UpdatedAt           time.Time
}

// OAuth is how people sign in to a connector's vendor. Empty endpoints are
// discovered from the MCP server.
type OAuth struct {
	Registration, ClientID            string
	SecretStored                      bool
	AuthorizeURL, TokenURL, RevokeURL string
}

// Tool is one tool the connector's MCP server listed. ReadOnlyHint is what
// the server claimed; it only prefills the admin's choice of mode.
type Tool struct {
	Name, Description string
	ReadOnlyHint      bool
}

// Bounds of what an admin may set.
const (
	MaxURL       = 2048
	MaxNameRunes = 100
	MaxScopes    = 50
	MaxScope     = 200
	MaxGroups    = 100
	MaxGroup     = 200
	MaxToolModes = 500
)

// ToolSeparator joins a connector's slug and its tool's name on Tapas's MCP
// endpoint: slack__post_message. Tapas's own tools never contain it.
const ToolSeparator = "__"

// MaxToolName is the longest tool name an MCP client is given; longer
// joined names are left out.
const MaxToolName = 128

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9]{1,30}$`)

// ValidSlug reports whether s may be a connector's slug: 2 to 31 lowercase
// letters and digits, starting with a letter. It never contains the
// separator, so a joined name splits at its first one.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }

// JoinTool is the name a connector's tool has on Tapas's MCP endpoint, and
// false when tool is empty or the joined name is longer than MaxToolName.
func JoinTool(slug, tool string) (string, bool) {
	name := slug + ToolSeparator + tool
	if tool == "" || len(name) > MaxToolName {
		return "", false
	}
	return name, true
}

// SplitTool is the slug and the upstream tool of a joined name, split at
// the first separator; false when name is not a connector's tool.
func SplitTool(name string) (slug, tool string, ok bool) {
	slug, tool, ok = strings.Cut(name, ToolSeparator)
	if !ok || tool == "" || !ValidSlug(slug) {
		return "", "", false
	}
	return slug, tool, true
}

// FieldError is a refused field (named as in the admin API's body) and why.
type FieldError struct {
	Field string
	Code  reasons.MessageCode
	Args  []any
}

func (e *FieldError) Error() string { return e.Field + ": " + reasons.Msg(e.Code, e.Args...) }

// CheckURL refuses an address a connector may not be reached at: one that
// is not absolute, carries a user name, password or fragment, or is not
// https. Plain http is allowed for a host in internalHosts (in-cluster MCP
// servers). Where the address resolves to is checked when it is dialled.
func CheckURL(raw string, internalHosts []string) error {
	bad := &FieldError{Code: reasons.MsgConnectorURLNotAllowed}
	if raw == "" || len(raw) > MaxURL {
		return bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return bad
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if Internal(u.Hostname(), internalHosts) {
			return nil
		}
	}
	return bad
}

// Internal reports whether host is one of internalHosts, ignoring case.
func Internal(host string, internalHosts []string) bool {
	return slices.ContainsFunc(internalHosts, func(h string) bool { return strings.EqualFold(h, host) })
}

// SameOrigin reports whether a and b have the same scheme, host and port
// (a default port counts as given).
func SameOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Hostname(), ub.Hostname()) && port(ua) == port(ub)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// Validate refuses a connector an admin may not save, naming the first bad
// field as a *FieldError. internalHosts are the hosts plain http may name.
func (c Connector) Validate(internalHosts []string) error {
	field := func(name string, code reasons.MessageCode) error { return &FieldError{Field: name, Code: code} }
	if !ValidSlug(c.Slug) {
		return field("slug", reasons.MsgConnectorSlugInvalid)
	}
	if n := utf8.RuneCountInString(c.Name); strings.TrimSpace(c.Name) == "" || n > MaxNameRunes || strings.ContainsFunc(c.Name, unicode.IsControl) {
		return field("name", reasons.MsgConnectorNameInvalid)
	}
	if err := CheckURL(c.URL, internalHosts); err != nil {
		return field("url", reasons.MsgConnectorURLNotAllowed)
	}
	if c.AuthMode != AuthUser && c.AuthMode != AuthShared {
		return field("authMode", reasons.MsgConnectorAuthModeInvalid)
	}
	for _, e := range []struct{ name, url string }{
		{"oauth.authorizeUrl", c.OAuth.AuthorizeURL}, {"oauth.tokenUrl", c.OAuth.TokenURL}, {"oauth.revokeUrl", c.OAuth.RevokeURL},
	} {
		if e.url != "" && CheckURL(e.url, internalHosts) != nil {
			return field(e.name, reasons.MsgConnectorURLNotAllowed)
		}
	}
	if (c.OAuth.AuthorizeURL == "") != (c.OAuth.TokenURL == "") || c.OAuth.RevokeURL != "" && c.OAuth.TokenURL == "" {
		return field("oauth.authorizeUrl", reasons.MsgConnectorEndpointsPaired)
	}
	if !words(c.Scopes, MaxScopes, MaxScope) {
		return field("scopes", reasons.MsgConnectorScopesInvalid)
	}
	if !words(c.AllowedGroups, MaxGroups, MaxGroup) {
		return field("allowedGroups", reasons.MsgConnectorGroupsInvalid)
	}
	if len(c.ToolModes) > MaxToolModes {
		return field("toolModes", reasons.MsgConnectorToolModesInvalid)
	}
	for tool, m := range c.ToolModes {
		if tool == "" || len(tool) > MaxToolName || (m != ToolRead && m != ToolWrite && m != ToolOff) {
			return field("toolModes", reasons.MsgConnectorToolModesInvalid)
		}
	}
	return nil
}

// words reports whether list has at most n entries, each 1 to size bytes of
// printable text without spaces.
func words(list []string, n, size int) bool {
	if len(list) > n {
		return false
	}
	for _, w := range list {
		if w == "" || len(w) > size || strings.ContainsFunc(w, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) {
			return false
		}
	}
	return true
}

// SealContext is the associated data a connector's value is sealed with
// under the gateway's sessions key: it binds the ciphertext to its table,
// row and column, so one copied to another row or column does not open.
func SealContext(table, rowID, column string) []byte {
	return []byte("javo connector v1\x00" + table + "\x00" + rowID + "\x00" + column)
}
