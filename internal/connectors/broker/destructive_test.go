package broker

import (
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
)

// As the MCP spec reads the hints, except that a tool the admin set to
// write is never taken as read-only on the connector's word: only an
// explicit destructiveHint false says it can be undone.
func TestDestructiveFollowsTheSpecsDefaults(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	for _, c := range []struct {
		name string
		mode connectors.ToolMode
		a    *sdk.ToolAnnotations
		want bool
	}{
		{"write, no hints", connectors.ToolWrite, nil, true},
		{"write, empty hints", connectors.ToolWrite, &sdk.ToolAnnotations{}, true},
		{"write, says destructive", connectors.ToolWrite, &sdk.ToolAnnotations{DestructiveHint: &yes}, true},
		{"write, says not destructive", connectors.ToolWrite, &sdk.ToolAnnotations{DestructiveHint: &no}, false},
		{"write, says it only reads", connectors.ToolWrite, &sdk.ToolAnnotations{ReadOnlyHint: true}, true},
		{"read the policy sent for approval, only reads", connectors.ToolRead, &sdk.ToolAnnotations{ReadOnlyHint: true}, false},
		{"read the policy sent for approval, no hints", connectors.ToolRead, nil, true},
	} {
		if got := destructive(c.mode, &sdk.Tool{Name: "t", Annotations: c.a}); got != c.want {
			t.Errorf("%s: destructive = %v, want %v", c.name, got, c.want)
		}
	}
}
