package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/nebari-dev/javo/internal/actions"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/policy"
	"github.com/nebari-dev/javo/internal/connectors/registry"
)

// as is the connector-call action acting for c.
func as(f *fixture, c connectors.Caller) actions.Action {
	return f.b.Action(func(context.Context) connectors.Caller { return c })
}

// owner is alice in her browser session: how a plan is approved.
func owner() connectors.Caller {
	c := alice()
	c.Token, c.Client = token.Token{}, ""
	return c
}

func writer() connectors.Caller {
	c := alice()
	c.Token.Scope = token.ScopePropose
	return c
}

func proposal(args string) actions.Proposal {
	return actions.Proposal{Action: actions.ConnectorCall, Target: "fake__post", Params: map[string]string{"arguments": args}}
}

func status(t *testing.T, err error) int {
	t.Helper()
	var se huma.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error %v has no status", err)
	}
	return se.GetStatus()
}

// Planning a call checks it and shows every argument as it will be sent,
// without reaching the connector.
func TestPlanningACallShowsItsExactArguments(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	pl, err := as(f, writer()).Plan(ctx, proposal(`{ "channel": "c-1", "text": "hello",
		"blocks": [ {"type": "section"} ] }`))
	if err != nil {
		t.Fatal(err)
	}
	if pl.Summary != "Fake: post" || pl.Owner != "alice" || pl.TargetName != "Fake" || !pl.Destructive ||
		pl.Params["arguments"] != `{"channel":"c-1","text":"hello","blocks":[{"type":"section"}]}` {
		t.Fatalf("planned %+v", pl)
	}
	want := []actions.Change{{Field: "channel", To: `"c-1"`}, {Field: "text", To: `"hello"`}, {Field: "blocks", To: `[{"type":"section"}]`}}
	if len(pl.Changes) != len(want) {
		t.Fatalf("changes %+v", pl.Changes)
	}
	for i := range want {
		if pl.Changes[i] != want[i] {
			t.Errorf("change %d: %+v, want %+v", i, pl.Changes[i], want[i])
		}
	}
	if len(f.fake.Calls()) != 0 {
		t.Fatalf("calls %v", f.fake.Calls())
	}
}

// Planning repeats every check of a call, as the person in the request.
func TestPlanningRepeatsTheCallsChecks(t *testing.T) {
	t.Parallel()
	bob := writer()
	bob.Principal = authz.Principal{ID: "bob", Groups: []string{"support"}}
	app := writer()
	app.Token.App = &token.App{Namespace: "ns", Name: "helper-1", Short: "helper"}
	var seen policy.Input
	cases := []struct {
		name   string
		o      options
		caller connectors.Caller
		p      actions.Proposal
		want   int
	}{
		{"outside the groups", options{}, bob, proposal(`{}`), http.StatusNotFound},
		{"an app's token", options{}, app, proposal(`{}`), http.StatusForbidden},
		{"a browser session", options{}, owner(), proposal(`{}`), http.StatusForbidden},
		{"unknown tool", options{}, writer(), actions.Proposal{Action: actions.ConnectorCall, Target: "fake__nope"}, http.StatusNotFound},
		{"another owner", options{}, writer(), actions.Proposal{Action: actions.ConnectorCall, Target: "fake__post", Owner: "bob"}, http.StatusNotFound},
		{"not an object", options{}, writer(), proposal(`[1]`), http.StatusUnprocessableEntity},
		{"a field twice", options{}, writer(), proposal(`{"text":"hi","text":"bye"}`), http.StatusUnprocessableEntity},
		{"policy denies", options{policy: denyAll{&seen}}, writer(), proposal(`{}`), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, tc.o)
			_, err := as(f, tc.caller).Plan(ctx, tc.p)
			if got := status(t, err); got != tc.want {
				t.Fatalf("status %d (%v), want %d", got, err, tc.want)
			}
		})
	}
	if seen.Phase != policy.PhasePropose || !seen.Write || seen.Tool != "post" {
		t.Fatalf("policy saw %+v", seen)
	}
}

// Applying sends exactly the stored arguments once, with the connection's
// token, and audits the call.
func TestApplyingSendsTheStoredArgumentsOnce(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	pl, err := as(f, writer()).Plan(ctx, proposal(`{"text": "hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := as(f, owner()).Apply(ctx, pl.Proposal); err != nil {
		t.Fatal(err)
	}
	calls := f.fake.Calls()
	if len(calls) != 1 || calls[0].Tool != "post" || calls[0].Bearer != bearer || string(calls[0].Arguments) != pl.Params["arguments"] {
		t.Fatalf("calls %+v", calls)
	}
	rows := callRows(t, f.db)
	var d map[string]any
	if len(rows) != 1 || json.Unmarshal(rows[0].Detail, &d) != nil || d["outcome"] != "ok" || d["mode"] != "write" || d["policy"] != "allow" ||
		rows[0].Actor != "alice" || !rows[0].Allowed {
		t.Fatalf("rows %+v", rows)
	}
}

// Anything that changed since the plan was made stops it before the
// connector: the plan fails with the reason.
func TestApplyingChecksAgain(t *testing.T) {
	t.Parallel()
	pl := func(t *testing.T, f *fixture) actions.Proposal {
		p, err := as(f, writer()).Plan(ctx, proposal(`{"text":"hello"}`))
		if err != nil {
			t.Fatal(err)
		}
		return p.Proposal
	}
	t.Run("disabled", func(t *testing.T) {
		f := newFixture(t, options{})
		p := pl(t, f)
		if _, err := f.reg.Update(ctx, admin, f.conn.ID, registry.Input{
			Slug: "fake", Name: "Fake", URL: f.fake.URL, AuthMode: connectors.AuthShared, Enabled: false, Rev: f.conn.Rev,
			AllowedGroups: []string{"sales"}, ToolModes: f.conn.ToolModes,
		}); err != nil {
			t.Fatal(err)
		}
		if err := as(f, owner()).Apply(ctx, p); err == nil || len(f.fake.Calls()) != 0 {
			t.Fatalf("applied: %v, calls %v", err, f.fake.Calls())
		}
	})
	// The person approved a call to one server: a connector moved to
	// another address, or deleted and made again under the same short name,
	// is not that one.
	t.Run("moved", func(t *testing.T) {
		f := newFixture(t, options{})
		p := pl(t, f)
		elsewhere := connectorstest.NewConnector(t, bearer)
		if _, err := f.reg.Update(ctx, admin, f.conn.ID, registry.Input{
			Slug: "fake", Name: "Fake", URL: elsewhere.URL, AuthMode: connectors.AuthShared, Enabled: true, Rev: f.conn.Rev,
			AllowedGroups: []string{"sales"}, ToolModes: f.conn.ToolModes,
		}); err != nil {
			t.Fatal(err)
		}
		if err := as(f, owner()).Apply(ctx, p); err == nil || len(f.fake.Calls()) != 0 || len(elsewhere.Calls()) != 0 {
			t.Fatalf("applied: %v, calls %v and %v", err, f.fake.Calls(), elsewhere.Calls())
		}
	})
	t.Run("made again", func(t *testing.T) {
		f := newFixture(t, options{})
		p := pl(t, f)
		if err := f.reg.Delete(ctx, admin, f.conn.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.reg.Create(ctx, admin, registry.Input{
			Slug: "fake", Name: "Fake", URL: f.fake.URL, AuthMode: connectors.AuthShared, Enabled: true,
			AllowedGroups: []string{"sales"}, ToolModes: f.conn.ToolModes,
		}); err != nil {
			t.Fatal(err)
		}
		if err := as(f, owner()).Apply(ctx, p); err == nil || len(f.fake.Calls()) != 0 {
			t.Fatalf("applied: %v, calls %v", err, f.fake.Calls())
		}
	})
	t.Run("policy", func(t *testing.T) {
		checker := &phased{deny: policy.PhaseApply}
		f := newFixture(t, options{policy: checker})
		p := pl(t, f)
		err := as(f, owner()).Apply(ctx, p)
		if err == nil || !strings.Contains(err.Error(), "not now") || len(f.fake.Calls()) != 0 {
			t.Fatalf("applied: %v, calls %v", err, f.fake.Calls())
		}
		var d map[string]any
		if rows := callRows(t, f.db); len(rows) != 1 || json.Unmarshal(rows[0].Detail, &d) != nil || d["outcome"] != "denied_policy" {
			t.Fatalf("rows %+v", rows)
		}
	})
	t.Run("approval is not enough", func(t *testing.T) {
		f := newFixture(t, options{policy: &phased{ask: policy.PhaseApply}})
		if err := as(f, owner()).Apply(ctx, pl(t, f)); err == nil || len(f.fake.Calls()) != 0 {
			t.Fatalf("applied: %v", err)
		}
	})
	t.Run("someone else", func(t *testing.T) {
		f := newFixture(t, options{})
		bob := owner()
		bob.Principal = authz.Principal{ID: "bob", Groups: []string{"sales"}}
		if err := as(f, bob).Apply(ctx, pl(t, f)); err == nil || len(f.fake.Calls()) != 0 {
			t.Fatalf("applied: %v", err)
		}
	})
}

// A tool error from the connector fails the plan, without the connector's
// own words, which could hold anything.
func TestAToolErrorFailsThePlan(t *testing.T) {
	t.Parallel()
	f := newFixture(t, options{})
	pl, err := as(f, writer()).Plan(ctx, proposal(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	f.fake.ToolError(true)
	err = as(f, owner()).Apply(ctx, pl.Proposal)
	if err == nil || strings.Contains(err.Error(), "channel not found") || len(f.fake.Calls()) != 1 {
		t.Fatalf("applied: %v", err)
	}
}

// phased denies at phase deny and asks for approval at phase ask.
type phased struct{ deny, ask policy.Phase }

func (p *phased) Check(_ context.Context, in policy.Input) (policy.Decision, error) {
	switch in.Phase {
	case p.deny:
		return policy.Decision{Effect: policy.Deny, Reason: "Posting is not now."}, nil
	case p.ask:
		return policy.Decision{Effect: policy.RequireApproval}, nil
	}
	return policy.Decision{Effect: policy.Allow}, nil
}
