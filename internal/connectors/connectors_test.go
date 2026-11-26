package connectors_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/reasons"
)

func TestSlugPattern(t *testing.T) {
	for slug, ok := range map[string]bool{
		"slack": true, "gmail2": true, "ab": true, strings.Repeat("a", 31): true,
		"a": false, strings.Repeat("a", 32): false, "Slack": false, "2fa": false, "google-gmail": false,
		"my_tool": false, "": false, "sl ack": false,
	} {
		if got := connectors.ValidSlug(slug); got != ok {
			t.Errorf("ValidSlug(%q) = %v, want %v", slug, got, ok)
		}
	}
}

func TestToolNamesJoinAndSplitAtTheFirstSeparator(t *testing.T) {
	name, ok := connectors.JoinTool("slack", "post_message")
	if !ok || name != "slack"+connectors.ToolSeparator+"post_message" {
		t.Fatalf("JoinTool = %q, %v", name, ok)
	}
	slug, tool, ok := connectors.SplitTool(name)
	if !ok || slug != "slack" || tool != "post_message" {
		t.Fatalf("SplitTool(%q) = %q, %q, %v", name, slug, tool, ok)
	}
	// An upstream tool may itself contain the separator: the first one splits.
	slug, tool, ok = connectors.SplitTool("fake" + connectors.ToolSeparator + "a" + connectors.ToolSeparator + "b")
	if !ok || slug != "fake" || tool != "a"+connectors.ToolSeparator+"b" {
		t.Fatalf("SplitTool with two separators = %q, %q, %v", slug, tool, ok)
	}
	for _, bad := range []string{"list_apps", "", connectors.ToolSeparator + "echo", "fake" + connectors.ToolSeparator, "Fake" + connectors.ToolSeparator + "echo"} {
		if s, tl, ok := connectors.SplitTool(bad); ok {
			t.Errorf("SplitTool(%q) = %q, %q, true", bad, s, tl)
		}
	}
	if _, ok := connectors.JoinTool("fake", strings.Repeat("t", connectors.MaxToolName)); ok {
		t.Error("JoinTool accepted a name longer than MaxToolName")
	}
	if _, ok := connectors.JoinTool("fake", ""); ok {
		t.Error("JoinTool accepted an empty tool name")
	}
}

func TestModeOfAToolMissingFromTheMapIsWrite(t *testing.T) {
	modes := map[string]connectors.ToolMode{"echo": connectors.ToolRead, "drop": connectors.ToolOff}
	for tool, want := range map[string]connectors.ToolMode{"echo": connectors.ToolRead, "drop": connectors.ToolOff, "post": connectors.ToolWrite} {
		if got := connectors.ModeOf(modes, tool); got != want {
			t.Errorf("ModeOf(%q) = %q, want %q", tool, got, want)
		}
	}
	// An unknown stored mode is a write too: fail closed.
	if got := connectors.ModeOf(map[string]connectors.ToolMode{"echo": "maybe"}, "echo"); got != connectors.ToolWrite {
		t.Errorf("ModeOf with an unknown stored mode = %q", got)
	}
}

func TestCheckURL(t *testing.T) {
	internal := []string{"mcp.tapas.svc", "Slack-MCP.Example.org"}
	for raw, ok := range map[string]bool{
		"https://mcp.example.org/mcp":          true,
		"https://mcp.example.org:8443/mcp":     true,
		"http://mcp.tapas.svc:8080/mcp":        true,
		"http://slack-mcp.example.org/mcp":     true,
		"http://mcp.example.org/mcp":           false,
		"ftp://mcp.example.org/mcp":            false,
		"https://user:pw@mcp.example.org/mcp":  false,
		"https://mcp.example.org/mcp#fragment": false,
		"mcp.example.org/mcp":                  false,
		"https:///mcp":                         false,
		"":                                     false,
		"https://mcp.example.org/" + strings.Repeat("a", connectors.MaxURL): false,
	} {
		err := connectors.CheckURL(raw, internal)
		if (err == nil) != ok {
			t.Errorf("CheckURL(%q) = %v, want ok %v", raw, err, ok)
		}
	}
}

func TestSameOrigin(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"https://mcp.example.org/a", "https://mcp.example.org/b?x=1", true},
		{"https://mcp.example.org/a", "https://MCP.example.org:443/a", true},
		{"https://mcp.example.org/a", "https://mcp.example.org:8443/a", false},
		{"https://mcp.example.org/a", "http://mcp.example.org/a", false},
		{"https://mcp.example.org/a", "https://other.example.org/a", false},
	} {
		if got := connectors.SameOrigin(c.a, c.b); got != c.want {
			t.Errorf("SameOrigin(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestValidateNamesTheFirstBadField(t *testing.T) {
	good := connectors.Connector{
		Slug: "fake", Name: "Fake", URL: "https://mcp.example.org/mcp", AuthMode: connectors.AuthUser,
		Scopes: []string{"read"}, AllowedGroups: []string{"sales", "*"},
		ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}
	if err := good.Validate(nil); err != nil {
		t.Fatalf("Validate(good) = %v", err)
	}
	for field, mutate := range map[string]func(*connectors.Connector){
		"slug":               func(c *connectors.Connector) { c.Slug = "Fake" },
		"name":               func(c *connectors.Connector) { c.Name = " " },
		"url":                func(c *connectors.Connector) { c.URL = "http://mcp.example.org/mcp" },
		"authMode":           func(c *connectors.Connector) { c.AuthMode = "everyone" },
		"oauth.authorizeUrl": func(c *connectors.Connector) { c.OAuth.AuthorizeURL = "http://idp.example.org/authorize" },
		"oauth.tokenUrl":     func(c *connectors.Connector) { c.OAuth.TokenURL = "not a url" },
		"oauth.revokeUrl":    func(c *connectors.Connector) { c.OAuth.RevokeURL = "https://idp.example.org/revoke#x" },
		"scopes":             func(c *connectors.Connector) { c.Scopes = []string{"chat write"} },
		"allowedGroups":      func(c *connectors.Connector) { c.AllowedGroups = []string{""} },
		"toolModes":          func(c *connectors.Connector) { c.ToolModes = map[string]connectors.ToolMode{"echo": "maybe"} },
	} {
		c := good
		c.ToolModes = map[string]connectors.ToolMode{"echo": connectors.ToolRead}
		mutate(&c)
		var fe *connectors.FieldError
		if err := c.Validate(nil); !errors.As(err, &fe) || fe.Field != field || reasons.Msg(fe.Code) == reasons.Msg(reasons.MsgInvalidRequest) {
			t.Errorf("%s: Validate = %v", field, err)
		}
	}
	// A token endpoint set alone needs its authorize endpoint: static
	// endpoints come as a pair.
	c := good
	c.OAuth.TokenURL = "https://idp.example.org/token"
	var fe *connectors.FieldError
	if err := c.Validate(nil); !errors.As(err, &fe) || fe.Field != "oauth.authorizeUrl" {
		t.Errorf("token URL without authorize URL: %v", err)
	}
}

// Only a personal token or a coding agent's app token may reach
// connectors: a plain app's or a job's token is refused (D15).
func TestOnlyPersonalAndAgentTokensMayUseConnectors(t *testing.T) {
	app := &token.App{Namespace: "ns", Name: "helper-1", Short: "helper"}
	for name, c := range map[string]struct {
		caller connectors.Caller
		want   bool
	}{
		"personal":        {connectors.Caller{Token: token.Token{ID: "t-1", Owner: "alice"}}, true},
		"agent app":       {connectors.Caller{Token: token.Token{ID: "t-1", Owner: "alice", App: app}, AgentApp: true}, true},
		"plain app":       {connectors.Caller{Token: token.Token{ID: "t-1", Owner: "alice", App: app}}, false},
		"job":             {connectors.Caller{Token: token.Token{ID: "t-1", Owner: "alice", Job: "j-1"}, AgentApp: true}, false},
		"no token at all": {connectors.Caller{}, false},
	} {
		if got := c.caller.MayUseConnectors(); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
