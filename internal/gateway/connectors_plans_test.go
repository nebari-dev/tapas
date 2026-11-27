//go:build !local

package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/plans"
	"github.com/nebari-dev/javo/internal/reasons"
	"github.com/nebari-dev/javo/internal/store"
)

type writesFixture struct {
	*mcpFixture
	fake  *connectorstest.Connector
	clock *apitest.TestClock
	conn  connectors.Connector
	in    registry.Input
}

// newWritesFixture is a gateway with the fake connector added for everyone,
// its echo a read and its post a write.
func newWritesFixture(t *testing.T) *writesFixture {
	t.Helper()
	clk := &apitest.TestClock{T: time.Now()}
	fake := connectorstest.NewConnector(t, "tok-fake")
	af := apitest.New(t, apitest.Options{
		Mcp: true, Connectors: true, ConnectorInternalHosts: []string{"127.0.0.1"}, Now: clk.Now,
		ConnectorTokens: connectorstest.StaticToken("tok-fake"), ConnectorHTTP: &http.Client{}, PublicURL: "https://tapas.example.org",
	})
	s := httptest.NewServer(af.Srv)
	t.Cleanup(s.Close)
	f := &writesFixture{mcpFixture: &mcpFixture{Fixture: af, url: s.URL, alice: af.LoginAlice()}, fake: fake, clock: clk}
	f.in = registry.Input{
		Slug: "fake", Name: "Fake", URL: fake.URL, Enabled: true,
		AllowedGroups: []string{"*"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}
	conn, err := f.Connectors.Create(context.Background(), model.Actor{Person: "admin"}, f.in)
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	return f
}

// propose calls fake__post with args through /mcp with a token of scope
// and answers the plan handle.
func (f *writesFixture) propose(t *testing.T, scope string, args map[string]any) string {
	t.Helper()
	tok := f.NewToken(f.alice, `{"name":"agent","scope":"`+scope+`"}`).Token
	res, text := callTool(t, f.connect(t, tok), "fake__post", args)
	var out struct{ PlanID, State, ApproveURL string }
	if res.IsError || json.Unmarshal([]byte(text), &out) != nil || out.PlanID == "" || out.State != "awaiting_approval" ||
		out.ApproveURL != "https://tapas.example.org/approvals/"+out.PlanID {
		t.Fatalf("post: %v %q", res.IsError, text)
	}
	return out.PlanID
}

func (f *writesFixture) decide(t *testing.T, id, verb string, want int) apitest.PlanJSON {
	t.Helper()
	w := f.Do("POST", "/api/plans/"+id+"/"+verb, "", f.alice)
	if w.Code != want {
		t.Fatalf("%s: %d %s, want %d", verb, w.Code, w.Body, want)
	}
	return apitest.Decode[apitest.PlanJSON](t, w)
}

// rows are the audit rows of action.
func rows(t *testing.T, f *writesFixture, action string) []store.AuditEvent {
	t.Helper()
	all, err := f.Db.Audit().List(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, r := range all {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func detail(t *testing.T, r store.AuditEvent) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(r.Detail, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// An agent's connector write waits as a plan holding the exact call; its
// owner approves it in the browser and it is sent once, byte for byte; a
// second approval is "already done", and a token cannot approve. Both the
// proposal and the call are audited, naming the plan.
func TestAConnectorWriteIsAppliedOnceAfterApproval(t *testing.T) {
	t.Parallel()
	f := newWritesFixture(t)
	id := f.propose(t, "propose", map[string]any{"channel": "c-1", "text": "hello"})
	if n := len(f.fake.Calls()); n != 0 {
		t.Fatalf("proposing reached the connector %d times", n)
	}
	w := f.Do("GET", "/api/plans/"+id, "", f.alice)
	var got struct {
		State   string
		Planned struct {
			Action, Target, Summary string
			Params                  map[string]string
			Changes                 []struct{ Field, From, To string }
		}
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	args := got.Planned.Params["arguments"]
	if got.State != plans.Pending || got.Planned.Action != "connector-call" || got.Planned.Target != "fake__post" ||
		got.Planned.Summary != "Fake: post" || args != `{"channel":"c-1","text":"hello"}` || len(got.Planned.Changes) != 2 ||
		got.Planned.Changes[0].Field != "channel" || got.Planned.Changes[0].To != `"c-1"` {
		t.Fatalf("plan %+v", got)
	}
	proposed := rows(t, f, "connector.call")
	if len(proposed) != 1 || detail(t, proposed[0])["outcome"] != "proposed" || detail(t, proposed[0])["plan"] != plans.StoredID(id) {
		t.Fatalf("rows after proposing %+v", proposed)
	}

	tok := f.NewToken(f.alice, `{"name":"other","scope":"read-write"}`).Token
	if w := f.Bearer("POST", "/api/plans/"+id+"/approve", "", tok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("approve with a token: %d %s", w.Code, w.Body)
	}
	if n := len(f.fake.Calls()); n != 0 {
		t.Fatalf("a token's approval reached the connector %d times", n)
	}

	if p := f.decide(t, id, "approve", http.StatusOK); p.State != plans.Applied {
		t.Fatalf("approved: %+v", p)
	}
	calls := f.fake.Calls()
	if len(calls) != 1 || calls[0].Tool != "post" || calls[0].Bearer != "tok-fake" || string(calls[0].Arguments) != args {
		t.Fatalf("calls %+v", calls)
	}
	f.decide(t, id, "approve", http.StatusConflict)
	if n := len(f.fake.Calls()); n != 1 {
		t.Fatalf("a second approval made %d calls", n)
	}

	applied := rows(t, f, "connector.call")
	if len(applied) != 2 {
		t.Fatalf("connector.call rows %+v", applied)
	}
	if d := detail(t, applied[0]); d["outcome"] != "ok" || d["plan"] != plans.StoredID(id) || applied[0].Actor != apitest.LocalAlice() {
		t.Fatalf("applied row %+v", applied[0])
	}
	decided := rows(t, f, "ActionApplied")
	if len(decided) != 1 || decided[0].ObjectKind != "connector" || decided[0].ObjectName != "fake" || detail(t, decided[0])["tool"] != "post" {
		t.Fatalf("plan rows %+v", decided)
	}
}

// A rejected or expired plan never reaches the connector.
func TestRejectedAndExpiredWritesNeverReachTheConnector(t *testing.T) {
	t.Parallel()
	f := newWritesFixture(t)
	if p := f.decide(t, f.propose(t, "propose", map[string]any{"text": "no"}), "reject", http.StatusOK); p.State != plans.Rejected {
		t.Fatalf("rejected: %+v", p)
	}
	id := f.propose(t, "propose", map[string]any{"text": "late"})
	f.clock.Add(plans.DefaultTTL + time.Minute)
	f.decide(t, id, "approve", http.StatusGone)
	if n := len(f.fake.Calls()); n != 0 {
		t.Fatalf("the connector got %d calls", n)
	}
}

// A connector disabled after the proposal fails the plan when approved,
// before the connector.
func TestADisabledConnectorFailsItsPlans(t *testing.T) {
	t.Parallel()
	f := newWritesFixture(t)
	id := f.propose(t, "propose", map[string]any{"text": "hello"})
	f.in.Enabled, f.in.Rev = false, f.conn.Rev
	if _, err := f.Connectors.Update(context.Background(), model.Actor{Person: "admin"}, f.conn.ID, f.in); err != nil {
		t.Fatal(err)
	}
	f.decide(t, id, "approve", http.StatusNotFound)
	w := f.Do("GET", "/api/plans/"+id, "", f.alice)
	if p := apitest.Decode[apitest.PlanJSON](t, w); p.State != plans.Failed || len(f.fake.Calls()) != 0 {
		t.Fatalf("plan %+v, calls %v", p, f.fake.Calls())
	}
}

// A read-only token cannot propose a write: nothing is stored and the
// connector is not reached. The connector-call action is hidden, so it is
// no propose tool of its own.
func TestAReadTokenCannotProposeAWrite(t *testing.T) {
	t.Parallel()
	f := newWritesFixture(t)
	tok := f.NewToken(f.alice, `{"name":"agent","scope":"read"}`).Token
	res, text := callTool(t, f.connect(t, tok), "fake__post", map[string]any{"text": "hello"})
	if !res.IsError || text != reasons.Msg(reasons.MsgTokenReadOnly) {
		t.Fatalf("post: %v %q", res.IsError, text)
	}
	if names := toolNames(t, f.mcpFixture, tok); names["connector-call_workspace"] || !names["fake__post"] {
		t.Fatalf("tools %v", names)
	}
	w := f.Do("GET", "/api/plans", "", f.alice)
	if list := apitest.Decode[[]apitest.PlanJSON](t, w); len(list) != 0 || len(f.fake.Calls()) != 0 {
		t.Fatalf("plans %+v, calls %v", list, f.fake.Calls())
	}
}
