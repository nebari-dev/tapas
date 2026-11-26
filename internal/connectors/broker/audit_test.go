package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/client"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/policy"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

// failingCall is a forwarder whose tool calls fail with err; listing works.
type failingCall struct {
	client.Client
	err error
}

func (f *failingCall) CallTool(context.Context, client.Target, string, string, json.RawMessage) (*sdk.CallToolResult, error) {
	return nil, f.err
}

// stepClock answers t0 on its first reading and t0 plus 412 ms after, so
// a call lasts 412 ms.
func stepClock() func() time.Time {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := true
	return func() time.Time {
		if first {
			first = false
			return t0
		}
		return t0.Add(412 * time.Millisecond)
	}
}

// callRows are the connector.call rows in the audit log.
func callRows(t *testing.T, db *store.Store) []store.AuditEvent {
	t.Helper()
	all, err := db.Audit().List(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range all {
		if e.Action == "connector.call" {
			out = append(out, e)
		}
	}
	return out
}

// Every way a call ends writes exactly one row naming who called which
// connector's tool and how it ended.
func TestEachOutcomeWritesOneRow(t *testing.T) {
	t.Parallel()
	app := alice()
	app.Token.App = &token.App{Namespace: "ns", Name: "helper-1", Short: "helper"}
	bob := alice()
	bob.Principal = authz.Principal{ID: "bob", Groups: []string{"support"}}
	bob.Token = token.Token{ID: "t-2", Owner: "bob"}
	agent := app
	agent.AgentApp, agent.Agent = true, "helper"
	var seen policy.Input

	cases := []struct {
		name    string
		o       options
		caller  connectors.Caller
		tool    string
		args    string
		allowed bool
		detail  map[string]any
		noID    bool
	}{
		{name: "ok", caller: alice(), tool: "fake__echo", args: `{"text":"hi"}`, allowed: true,
			detail: map[string]any{"tool": "echo", "mode": "read", "outcome": "ok", "policy": "allow"}},
		{name: "agent", caller: agent, tool: "fake__echo", args: `{}`, allowed: true,
			detail: map[string]any{"tool": "echo", "mode": "read", "outcome": "ok", "policy": "allow", "agent": "helper-1"}},
		{name: "tool_error", caller: alice(), tool: "fake__echo", args: `[1]`, allowed: true,
			detail: map[string]any{"tool": "echo", "mode": "read", "outcome": "tool_error"}},
		{name: "proposed", caller: proposing(writer()), tool: "fake__post", args: `{}`, allowed: true,
			detail: map[string]any{"tool": "post", "mode": "write", "outcome": "proposed", "policy": "allow", "plan": plans.StoredID("h-1")}},
		{name: "read token proposing", caller: proposing(alice()), tool: "fake__post", args: `{}`, allowed: false,
			detail: map[string]any{"tool": "post", "mode": "write", "outcome": "denied_token"}},
		{name: "policy denies proposing", o: options{policy: &phased{deny: policy.PhasePropose}}, caller: proposing(writer()), tool: "fake__post", args: `{}`, allowed: false,
			detail: map[string]any{"tool": "post", "mode": "write", "outcome": "denied_policy", "policy": "deny"}},
		{name: "denied_token", caller: app, tool: "fake__echo", args: `{}`, allowed: false, noID: true,
			detail: map[string]any{"tool": "echo", "outcome": "denied_token", "agent": "helper-1", "credential": ""}},
		{name: "denied_authz", caller: bob, tool: "fake__echo", args: `{}`, allowed: false,
			detail: map[string]any{"tool": "echo", "outcome": "denied_authz"}},
		{name: "denied_policy", o: options{policy: denyAll{&seen}}, caller: alice(), tool: "fake__echo", args: `{}`, allowed: false,
			detail: map[string]any{"tool": "echo", "mode": "read", "outcome": "denied_policy", "policy": "deny"}},
		{name: "not_connected", o: options{tokens: connectorstest.StaticToken("")}, caller: alice(), tool: "fake__echo", args: `{}`, allowed: true,
			detail: map[string]any{"tool": "echo", "outcome": "not_connected"}},
		{name: "upstream_error", o: options{forwarder: &failingCall{err: client.ErrUpstream}}, caller: alice(), tool: "fake__echo", args: `{}`, allowed: true,
			detail: map[string]any{"tool": "echo", "mode": "read", "outcome": "upstream_error", "policy": "allow"}},
		{name: "timeout", o: options{forwarder: &failingCall{err: client.ErrTimeout}}, caller: alice(), tool: "fake__echo", args: `{}`, allowed: true,
			detail: map[string]any{"tool": "echo", "mode": "read", "outcome": "timeout", "policy": "allow"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.o.now = stepClock()
			f := newFixture(t, tc.o)
			c := tc.caller
			if c.Propose != nil {
				c.Propose = planWith(f)
			}
			call(t, f, c, tc.tool, tc.args)
			rows := callRows(t, f.db)
			if len(rows) != 1 {
				t.Fatalf("%d rows: %+v", len(rows), rows)
			}
			r := rows[0]
			who := tc.caller.Principal.ID
			wantID := f.conn.ID
			if tc.noID {
				wantID = ""
			}
			if r.Actor != who || r.Via != "token:"+tc.caller.Token.ID || r.ObjectKind != "connector" || r.ObjectName != "fake" ||
				r.ObjectID != wantID || r.ObjectOwner != who || r.Allowed != tc.allowed {
				t.Errorf("row %+v", r)
			}
			want := map[string]any{
				"tool": "", "mode": "", "outcome": "", "agent": "", "client": "claude-code",
				"credential": "shared", "policy": "", "ms": float64(412),
			}
			maps.Copy(want, tc.detail)
			var got map[string]any
			if err := json.Unmarshal(r.Detail, &got); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, want) {
				t.Errorf("detail\n got %v\nwant %v", got, want)
			}
		})
	}
}

// Listing tools records nothing.
func TestListingWritesNoRow(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	f.b.Tools(ctx, alice())
	if rows := callRows(t, f.db); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}
}

type brokenAudit struct{}

func (brokenAudit) Append(context.Context, store.AuditEvent) error { return errors.New("disk full") }

// When the row cannot be written the connector's answer is withheld and
// the call fails, so no unaudited data leaves.
func TestAFailedAuditWithholdsTheResult(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{audit: brokenAudit{}})
	res := call(t, f, alice(), "fake__echo", `{"text":"secret-payload"}`)
	if len(f.fake.Calls()) != 1 {
		t.Fatalf("calls %v", f.fake.Calls())
	}
	if !res.IsError || text(t, res) != reasons.Msg(reasons.MsgServerError) {
		t.Fatalf("result %+v", res)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "secret-payload") {
		t.Fatalf("result leaked: %s", b)
	}
}
