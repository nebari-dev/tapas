package gateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/memory"
	"github.com/nebari-dev/javo/internal/model"
)

// Only the own token of an app whose template has agentModel reaches the
// model endpoint: not a person's token, not another app's, not anonymous.
func TestOnlyAgentAppsReachTheModelEndpoint(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(up.Close)
	p := &agentproxy.Proxy{Upstream: apitest.FixedUpstream(agentproxy.Upstream{BaseURL: up.URL, APIKey: "sk-admin", Model: "m"}),
		MaxRequestBytes: 1 << 20}
	f := newAgentFixtureWith(t, true, apitest.Options{AgentModel: p})
	path := agentproxy.Route + "/chat/completions"
	body := `{"model":"m","messages":[]}`
	if w := f.agent("POST", path, body); w.Code != http.StatusOK {
		t.Fatalf("the agent's token: %d %s", w.Code, w.Body)
	}
	// Spend is capped at the provider: the daily allowance of the other AI
	// features is not touched.
	if used, err := f.Db.AIUsage().Used(context.Background(), apitest.LocalAlice(), time.Now().UTC().Format(time.DateOnly)); err != nil || used != 0 {
		t.Fatalf("ai_usage = %d, %v; want untouched", used, err)
	}
	personal, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{
		Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := f.Tokens.Create(context.Background(), model.Actor{System: model.SystemController}, token.Token{
		Name: "plain", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
		App: &token.App{Namespace: f.GetWorkspace(apitest.LocalAlice(), "plain").Namespace, Name: f.GetWorkspace(apitest.LocalAlice(), "plain").ObjectName(), Short: "plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for what, w := range map[string]*httptest.ResponseRecorder{
		"a personal token":   f.Bearer("POST", path, body, personal, nil),
		"an app's own token": f.Bearer("POST", path, body, plain, nil),
		"a browser session":  f.Do("POST", path, body, f.alice),
		"nobody":             f.Do("POST", path, body, nil),
	} {
		if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "sk-admin") {
			t.Errorf("%s: the key is in the answer", what)
		}
	}
	if calls != 1 {
		t.Fatalf("the provider was called %d times, want once", calls)
	}
}

// Taking the template away from the app's owner takes the model endpoint
// and the deploy rights away from the app they already have.
func TestLosingTheTemplateStopsTheAgentsRights(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(up.Close)
	p := &agentproxy.Proxy{Upstream: apitest.FixedUpstream(agentproxy.Upstream{BaseURL: up.URL, APIKey: "sk-admin", Model: "m"}),
		MaxRequestBytes: 1 << 20}
	// alice is a plain member here: a platform admin may use every template.
	f := newAgentFixtureWith(t, true, apitest.Options{AgentModel: p, Roles: &config.Roles{}})
	path := agentproxy.Route + "/chat/completions"
	wantCode(t, "the model while allowed", f.agent("POST", path, `{"model":"m","messages":[]}`), http.StatusOK)

	ctx := context.Background()
	pi, err := f.Db.Templates().Get(ctx, "pi")
	if err != nil {
		t.Fatal(err)
	}
	pi.Spec.AllowedUsers = []string{"someone-else"}
	if _, err := f.Db.Templates().Put(ctx, model.Actor{System: model.SystemController}, pi, pi.Version); err != nil {
		t.Fatal(err)
	}
	if w := f.agent("POST", path, `{"model":"m","messages":[]}`); w.Code != http.StatusForbidden {
		t.Fatalf("the model after losing the template: %d %s", w.Code, w.Body)
	}
	if w := f.agent("PUT", "/api/workspaces/plain/env", `{"env":[{"name":"LEVEL","value":"2"}]}`); w.Code != http.StatusForbidden {
		t.Fatalf("a deploy after losing the template: %d %s", w.Code, w.Body)
	}
}

// searchSpy answers every search with one note per owner and records who was
// searched for.
type searchSpy struct {
	mu     sync.Mutex
	owners []string
}

func (s *searchSpy) Search(_ context.Context, owner, _, _ string, _ int) ([]memory.Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owners = append(s.owners, owner)
	return []memory.Memory{{Text: "note for " + owner}}, nil
}
func (s *searchSpy) Add(context.Context, string, string, []memory.Message) ([]memory.Memory, error) {
	return nil, nil
}
func (s *searchSpy) Remember(context.Context, string, string) ([]memory.Memory, error) {
	return nil, nil
}
func (s *searchSpy) searched() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.owners...)
}

// A coding agent's model call carries the memories of the person who owns
// the agent app, looked up by the person's identity name, never another's.
func TestAgentModelCallsCarryTheOwnersMemories(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	t.Cleanup(up.Close)
	spy := &searchSpy{}
	p := &agentproxy.Proxy{Upstream: apitest.FixedUpstream(agentproxy.Upstream{BaseURL: up.URL, APIKey: "sk-admin", Model: "m"}),
		MaxRequestBytes: 1 << 20}
	pi := apitest.AgentTemplate()
	pi.Name, pi.Spec.AgentModel, pi.Spec.Category = "pi", true, model.CategoryAgents
	alicePi := gatewaytest.Workspace("pi", apitest.AliceID, "pi")
	bobPi := gatewaytest.Workspace("pi", apitest.BobID, "pi")
	alicePi.Template, bobPi.Template = "pi", "pi"
	f := apitest.New(t, apitest.Options{AgentModel: p, Memory: spy, Engines: apitest.TestEngines(),
		People: []apitest.TestPerson{apitest.SignedIn("alice", time.Time{}), apitest.SignedIn("bob", time.Time{})}}, pi, alicePi, bobPi)
	tokenOf := func(owner string, app *model.App) string {
		text, _, err := f.Tokens.Create(context.Background(), model.Actor{System: model.SystemController}, token.Token{
			Name: "pi", Owner: owner, Scope: token.ScopeReadWrite,
			App: &token.App{Namespace: app.Namespace, Name: app.ObjectName(), Short: "pi"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	path := agentproxy.Route + "/chat/completions"
	body := `{"model":"m","messages":[{"role":"user","content":"which editor do I like"}]}`

	tests := []struct {
		name  string
		owner string
		app   *model.App
	}{
		{"alice's agent carries alice's memories", apitest.AliceID, alicePi},
		{"bob's agent carries bob's memories, never alice's", apitest.BobID, bobPi},
	}
	for i, tc := range tests {
		wantCode(t, tc.name, f.Bearer("POST", path, body, tokenOf(tc.owner, tc.app), nil), http.StatusOK)
		mu.Lock()
		if len(seen) != i+1 || !strings.Contains(seen[i], "note for "+tc.owner) {
			t.Fatalf("%s: upstream saw %v, want the note for %q", tc.name, seen, tc.owner)
		}
		mu.Unlock()
		got := spy.searched()
		if len(got) != i+1 || got[i] != tc.owner {
			t.Fatalf("%s: memory searched for %v, want %q last", tc.name, got, tc.owner)
		}
	}
}
