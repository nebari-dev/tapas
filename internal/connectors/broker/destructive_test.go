package broker

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// As the MCP spec reads the hints: a tool that only reads is not
// destructive; any other tool is unless it says it is not.
func TestDestructiveFollowsTheSpecsDefaults(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	for _, c := range []struct {
		name string
		a    *sdk.ToolAnnotations
		want bool
	}{
		{"no hints", nil, true},
		{"empty hints", &sdk.ToolAnnotations{}, true},
		{"says destructive", &sdk.ToolAnnotations{DestructiveHint: &yes}, true},
		{"says not destructive", &sdk.ToolAnnotations{DestructiveHint: &no}, false},
		{"only reads", &sdk.ToolAnnotations{ReadOnlyHint: true}, false},
	} {
		if got := destructive(&sdk.Tool{Name: "t", Annotations: c.a}); got != c.want {
			t.Errorf("%s: destructive = %v, want %v", c.name, got, c.want)
		}
	}
}
