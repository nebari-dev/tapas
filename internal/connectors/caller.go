package connectors

import (
	"context"
	"errors"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
)

// Caller is who calls a connector's tool, all from the verified Tapas
// token: nothing in a call's arguments or headers can name another person
// or agent.
type Caller struct {
	// Principal is the token's owner, from the store, and Roles theirs.
	Principal authz.Principal
	Roles     []authz.Role
	// Token is the token used; Token.App names an agent's app.
	Token token.Token
	// Agent is the coding agent app's short name, "" for a personal token.
	Agent string
	// AgentApp is true when the token's app is a coding agent.
	AgentApp bool
	// Client is what the MCP client calls itself; display only.
	Client string
}

// MayUseConnectors reports whether c's token may reach connectors at all:
// a personal token, or the token of a coding agent's app. Any other app's
// or job's token is refused, since people the app is shared with, or the
// job's code, could make it act.
func (c Caller) MayUseConnectors() bool {
	return c.Token.ID != "" && (!c.Token.Workload() || c.Token.App != nil && c.Token.Job == "" && c.AgentApp)
}

// TokenSource gives the bearer a connector's calls carry for subject (the
// person, or "" for a company-wide connection). An error wrapping
// ErrNotConnected means there is no usable connection.
type TokenSource interface {
	Token(ctx context.Context, c Connector, subject string) (string, error)
}

// ErrNotConnected is a connector with no usable connection for the subject.
var ErrNotConnected = errors.New("connectors: not connected")
