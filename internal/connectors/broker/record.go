package broker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/store"
)

// How a call ended, for its audit row.
const (
	outcomeOK            = "ok"
	outcomeToolError     = "tool_error"
	outcomeDeniedToken   = "denied_token"
	outcomeDeniedAuthz   = "denied_authz"
	outcomeDeniedPolicy  = "denied_policy"
	outcomeNotConnected  = "not_connected"
	outcomeUpstreamError = "upstream_error"
	outcomeTimeout       = "timeout"
	// outcomeProposed is a write that became a plan; nothing was forwarded.
	outcomeProposed = "proposed"
)

// actionCall is the audit action of a connector tool call.
const actionCall = "connector.call"

// auditTimeout bounds writing the row. The row is written even when the
// agent has gone, since the connector may already have acted.
const auditTimeout = 10 * time.Second

// callDetail is the detail of a call's audit row: names and the outcome
// only, never arguments, results, tokens or what a connector said.
type callDetail struct {
	Tool    string `json:"tool"`
	Mode    string `json:"mode"`
	Outcome string `json:"outcome"`
	// Agent is the coding agent app's object name, "" for a personal token.
	Agent  string `json:"agent"`
	Client string `json:"client"`
	// Credential is whose connection the connector uses: "user" or
	// "shared"; "" when the connector was not found.
	Credential string `json:"credential"`
	// Plan is the plan a write became.
	Plan string `json:"plan,omitempty"`
	// Policy is the policy's effect; "" when it was not asked.
	Policy string `json:"policy"`
	Ms     int64  `json:"ms"`
}

// record writes the call's audit row once its outcome is known and returns
// the answer to give. When the row cannot be written, res is withheld and
// the call answers an error (D19).
func (b *Broker) record(ctx context.Context, c *call, res *sdk.CallToolResult) *sdk.CallToolResult {
	if b.appendRow(ctx, c) != nil {
		return errorResult(c, c.outcome, serverError)
	}
	return res
}

// appendRow writes c's audit row; a failure is logged and returned.
func (b *Broker) appendRow(ctx context.Context, c *call) error {
	if b.c.Audit == nil {
		slog.Error("connector calls cannot be audited: no audit log is configured")
		return errNoAudit
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if err := b.c.Audit.Append(ctx, auditRow(c, b.c.Now())); err != nil {
		slog.Error("audit a connector call", "connector", c.conn.Slug, "outcome", c.outcome, "err", err)
		return err
	}
	return nil
}

var errNoAudit = errors.New("broker: no audit log")

// auditRow is c's audit row, at now.
func auditRow(c *call, now time.Time) store.AuditEvent {
	who, via := store.ActorStrings(model.Actor{Person: c.caller.Principal.ID, Token: c.caller.Token.ID})
	d := callDetail{
		Tool: c.tool, Mode: string(c.mode), Outcome: c.outcome, Client: c.caller.Client,
		Credential: string(c.conn.AuthMode), Plan: c.plan, Policy: string(c.policy), Ms: now.Sub(c.started).Milliseconds(),
	}
	if app := c.caller.Token.App; app != nil {
		d.Agent = app.Name
	}
	detail, _ := json.Marshal(d) // plain strings and a number: cannot fail
	return store.AuditEvent{
		Actor: who, Via: via, Action: actionCall, ObjectKind: "connector",
		ObjectName: c.conn.Slug, ObjectID: c.conn.ID, ObjectOwner: c.caller.Principal.ID,
		Allowed: !strings.HasPrefix(c.outcome, "denied_"), Detail: detail,
	}
}
