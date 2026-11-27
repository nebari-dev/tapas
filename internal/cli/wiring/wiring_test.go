package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/provider"
	"github.com/nebari-dev/javo/internal/auth/local"
	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/memory"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/secrets"
	"github.com/nebari-dev/javo/internal/storage/ids"
	"github.com/nebari-dev/javo/internal/store"
	"github.com/nebari-dev/javo/internal/store/storetest"
)

// With ai.enabled false, no AI settings exist and no provider is built, so
// nothing ever connects to a model, even with a provider and key configured.
// On, the provider is built from the config and its key on first use.
func TestAIOffBuildsNoProvider(t *testing.T) {
	var got []provider.Config
	var r provider.Registry
	r.Register("spy", func(c provider.Config) (provider.Provider, error) { got = append(got, c); return aitest.New(), nil })
	cfg := config.AICfg{Enabled: false, Provider: "spy", Model: "m"}
	db := storetest.New(t)
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if l := aiSettings(cfg, "a-key", db, key, &r); l != nil || len(got) != 0 {
		t.Fatalf("AI off made settings %v, factory called %d times", l, len(got))
	}
	cfg.Enabled = true
	l := aiSettings(cfg, "a-key", db, key, &r)
	if _, err := l.Backend(context.Background()); err != nil || len(got) != 1 || got[0].APIKey != "a-key" || got[0].Model != "m" {
		t.Fatalf("AI on: %v, built %+v", err, got)
	}
}

func TestAuthProvidersFollowTheConfig(t *testing.T) {
	var on, off config.Platform
	on.Auth.Local.Enabled = true
	users := &local.DBUsers{}

	if _, ok := AuthProviders(on, users, nil).Get("local"); !ok {
		t.Fatal("local provider missing with auth.local.enabled")
	}
	if n := len(AuthProviders(off, users, nil).All()); n != 0 {
		t.Fatalf("%d providers with auth.local disabled", n)
	}
}

func TestExposureBackendFollowsURLsMode(t *testing.T) {
	for mode, want := range map[string]string{"path": "path", "subdomain": "subdomain"} {
		var p config.Platform
		p.URLs.Mode = mode
		if got := Exposure(p).ID(); got != want {
			t.Errorf("urls.mode %q: backend %q", mode, got)
		}
	}
}

func TestPlacementFollowsNamespacesMode(t *testing.T) {
	for _, mode := range []string{"shared", "per-user", "per-team"} {
		var p config.Platform
		p.Namespaces.Mode = mode
		if got := Placement(p).ID(); got != mode {
			t.Errorf("namespaces.mode %q: placement %q", mode, got)
		}
	}
}

func TestEngineRegistryHoldsTheVerifiedEngines(t *testing.T) {
	r := Engines()
	var ids []string
	for _, e := range r.All() {
		ids = append(ids, e.ID())
	}
	if want := []string{"bash", "command", "marimo", "papermill", "quarto"}; !slices.Equal(ids, want) {
		t.Errorf("engines = %v, want %v", ids, want)
	}
	byExt := func(ext string) []string {
		var out []string
		for _, e := range r.All() {
			if slices.Contains(e.Extensions(), ext) {
				out = append(out, e.ID())
			}
		}
		return out
	}
	if got := byExt(".ipynb"); !slices.Equal(got, []string{"papermill", "quarto"}) {
		t.Errorf(".ipynb = %v", got)
	}
	// No engine runs .jl files: Pluto.jl was dropped (DEPENDENCIES.md).
	if got := byExt(".jl"); len(got) != 0 {
		t.Errorf(".jl = %v, want none", got)
	}
}

func TestStorageRegistryHoldsBothBackends(t *testing.T) {
	r := newStorageRegistry()
	for id, mode := range map[string]model.HomeMode{
		"storage.per-user": model.HomeModePerUser,
		"storage.shared":   model.HomeModeShared,
	} {
		b, ok := r.Get(id)
		if !ok || b.Mode() != mode {
			t.Fatalf("Get(%q) = %v, %v; want mode %s", id, b, ok, mode)
		}
	}
}

func TestStorageBackendsAreIndexedByTheModeTheyServe(t *testing.T) {
	m := StorageBackendsByMode()
	for _, mode := range []model.HomeMode{model.HomeModePerUser, model.HomeModeShared} {
		if b, ok := m[mode]; !ok || b.Mode() != mode {
			t.Errorf("mode %s: %v, %v", mode, b, ok)
		}
	}
}

func TestCodeHostsServeGitHubRepositories(t *testing.T) {
	hosts := CodeHosts(config.Platform{}).All()
	if len(hosts) != 1 || hosts[0].ID() != "github" {
		t.Fatalf("code hosts = %v", hosts)
	}
	if !hosts[0].Owns("https://github.com/example-org/site") || hosts[0].Owns("https://gitlab.com/example-org/site") {
		t.Fatal("the GitHub host does not own exactly the github.com repositories")
	}
}

// With nebi.enabled false the gateway gets no environment source, so no form
// offers environments and nothing talks to Nebi, even with a URL set.
func TestNebiOffWiresNoEnvironmentSource(t *testing.T) {
	p := config.Platform{Nebi: config.NebiCfg{URL: "https://nebi.example.com"}}
	if src := EnvironmentSource(p); src != nil {
		t.Fatalf("nebi off wired %T", src)
	}
	p.Nebi.Enabled = true
	if src := EnvironmentSource(p); src == nil || src.ID() != "nebi" {
		t.Fatalf("nebi on wired %v", src)
	}
}

// Apps get their owner's Nebi token only with nebi.enabled: off, the gateway
// keeps none and the controller gives no app Nebi's address.
func TestNebiOffGivesNoAppANebiToken(t *testing.T) {
	p := config.Platform{Nebi: config.NebiCfg{URL: "https://nebi.example.com"}}
	if k := NebiTokens(p, nil); k != nil || NebiURL(p) != "" {
		t.Fatalf("nebi off: keeper %v, url %q", k, NebiURL(p))
	}
	p.Nebi.Enabled = true
	if k := NebiTokens(p, nil); k == nil || k.Source == nil || NebiURL(p) != "https://nebi.example.com" {
		t.Fatalf("nebi on: keeper %v, url %q", k, NebiURL(p))
	}
}

// Each limits key reaches the store's cap of the same name.
func TestStoreLimitsFollowTheConfig(t *testing.T) {
	got := StoreLimits(config.LimitsCfg{RunningApps: 1, Apps: 2, Previews: 3, Jobs: 4,
		ActiveRunsPerJob: 5, ActiveRunsPerPerson: 6, APITokens: 7, PendingPlans: 8})
	want := store.Limits{RunningApps: 1, Apps: 2, Previews: 3, Jobs: 4, RunsPerJob: 5, RunsPerPerson: 6, APITokens: 7, PendingPlans: 8}
	if got != want {
		t.Fatalf("StoreLimits = %+v, want %+v", got, want)
	}
}

// sessions.ttl is how long a sign-in lasts: the gateway refuses a session
// once that long has passed since it was made.
func TestASessionEndsAfterSessionsTTL(t *testing.T) {
	var p config.Platform
	p.Sessions.TTL.Duration = time.Hour
	now := time.Now()
	f := apitest.New(t, apitest.Options{
		Now:      func() time.Time { return now },
		Sessions: func(db *store.Store) *session.DBStore { return Sessions(p, db, nil) },
	})
	alice := f.Session(apitest.AliceID)
	now = now.Add(59 * time.Minute)
	if w := f.Do("GET", "/api/me", "", alice); w.Code != http.StatusOK {
		t.Fatalf("after 59m: %d %s", w.Code, w.Body)
	}
	now = now.Add(time.Minute)
	if w := f.Do("GET", "/api/me", "", alice); w.Code != http.StatusUnauthorized {
		t.Fatalf("after 1h: %d %s", w.Code, w.Body)
	}
}

// What a person's job runs and apps mount follows their groups only while
// the identity provider confirmed them within api.tokens.groupsMaxAge, as
// for their API tokens: someone taken out of a group, who never signs in
// again, does not keep the group's folder through a scheduled job.
func TestStaleGroupsMountNoGroupFolder(t *testing.T) {
	ctx := context.Background()
	db := storetest.New(t)
	p, err := config.Parse("config", []byte("auth:\n  local:\n    enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	claims := store.Claims{Issuer: "https://idp.example.com", Subject: "s-a", Username: "alice", Groups: []string{"research"}}
	alice, err := db.People(store.IDRange{}).Ensure(ctx, claims)
	if err != nil {
		t.Fatal(err)
	}
	f, err := db.SharedFolders().Create(ctx, model.Actor{Person: "admin-1"}, model.SharedFolder{Namespace: "javo-apps", Name: "research", Spec: model.SharedFolderSpec{Group: "research"}})
	if err != nil {
		t.Fatal(err)
	}
	f.Status = model.SharedFolderStatus{ClaimName: "folder-research", GID: 5001, Prepared: true,
		Conditions: []metav1.Condition{{Type: model.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}}
	if _, err := db.SharedFolders().Update(ctx, f, f.Version); err != nil {
		t.Fatal(err)
	}

	r := StorageResolver(db, p)
	groups := r.Groups.(ids.PersonGroups)
	now := time.Now()
	groups.Now = func() time.Time { return now }
	r.Groups = groups
	mounted := func() int {
		t.Helper()
		in, err := r.Input(ctx, "javo-apps", alice.Name)
		if err != nil {
			t.Fatal(err)
		}
		gids, err := r.FolderGIDs(ctx, "javo-apps", alice.Name)
		if err != nil || len(gids) != len(in.Folders) {
			t.Fatalf("gids %v for %d folders, %v", gids, len(in.Folders), err)
		}
		return len(in.Folders)
	}
	now = now.Add(6 * 24 * time.Hour)
	if n := mounted(); n != 1 {
		t.Fatalf("6 days after sign-in: %d group folders", n)
	}
	now = now.Add(2 * 24 * time.Hour)
	if n := mounted(); n != 0 {
		t.Fatalf("8 days after sign-in: %d group folders", n)
	}
	// Signing in again (the people table records it on the real clock)
	// brings the folder back.
	if _, err := db.People(store.IDRange{}).Ensure(ctx, claims); err != nil {
		t.Fatal(err)
	}
	now = time.Now().Add(time.Minute)
	if n := mounted(); n != 1 {
		t.Fatalf("after signing in again: %d group folders", n)
	}
}

// With memory off no service is built, so nothing ever talks to mem0. With it
// on, the service sends its requests to the configured address with the key.
func TestMemoryIsNilWhenOffAndTalksToTheConfiguredURLWithTheKeyWhenOn(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("X-API-Key"), r.URL.Path
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	on := config.MemoryCfg{
		Enabled: true, URL: srv.URL,
		Timeout: metav1.Duration{Duration: time.Second}, AddTimeout: metav1.Duration{Duration: time.Minute},
	}
	if got := Memory(config.MemoryCfg{Enabled: false, URL: srv.URL}, "k"); got != nil {
		t.Fatalf("Memory with memory off = %v, want nil", got)
	}
	svc := Memory(on, "the-admin-key")
	if svc == nil {
		t.Fatal("Memory with memory on = nil")
	}
	if _, err := svc.List(context.Background(), "alice"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotKey != "the-admin-key" || gotPath != "/memories" {
		t.Errorf("mem0 saw key %q on path %q, want the key on /memories", gotKey, gotPath)
	}
}

// The wired memory service sends Tapas's extraction instructions on every
// add, and refuses a write holding an SSN before anything reaches mem0, on
// both the add path and the explicit remember path.
func TestMemoryRefusesWritesWithAnSSNAndSendsTheInstructions(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		bodies = append(bodies, b)
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	svc := Memory(config.MemoryCfg{
		Enabled:    true,
		URL:        srv.URL,
		Timeout:    metav1.Duration{Duration: 5 * time.Second},
		AddTimeout: metav1.Duration{Duration: 5 * time.Second},
	}, "k")
	if svc == nil {
		t.Fatal("Memory returned nil with memory enabled")
	}
	ctx := context.Background()
	const ssn = "my SSN is 123-45-6789"

	tests := []struct {
		name        string
		call        func() error
		wantRefused bool
		wantPrompt  bool
	}{
		{"add with an SSN is refused", func() error {
			_, err := svc.Add(ctx, "alice", "", []memory.Message{{Role: "user", Text: ssn}})
			return err
		}, true, false},
		{"remember with an SSN is refused", func() error {
			_, err := svc.Remember(ctx, "alice", ssn)
			return err
		}, true, false},
		{"a clean add carries the extraction instructions", func() error {
			_, err := svc.Add(ctx, "alice", "", []memory.Message{{Role: "user", Text: "I prefer Go"}})
			return err
		}, false, true},
		{"a clean remember carries no instructions", func() error {
			_, err := svc.Remember(ctx, "alice", "I prefer Go")
			return err
		}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bodies = nil
			err := tt.call()
			if tt.wantRefused {
				if !errors.Is(err, memory.ErrWriteRefused) {
					t.Fatalf("error = %v, want ErrWriteRefused", err)
				}
				if len(bodies) != 0 {
					t.Fatalf("%d requests reached mem0, want none", len(bodies))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 1 {
				t.Fatalf("%d requests, want 1", len(bodies))
			}
			prompt, has := bodies[0]["prompt"]
			if has != tt.wantPrompt {
				t.Fatalf("prompt present = %v, want %v", has, tt.wantPrompt)
			}
			if tt.wantPrompt && prompt != extractionInstructions {
				t.Errorf("prompt = %q, want the extraction instructions", prompt)
			}
		})
	}
}

// The agent model proxy carries memory only when a memory service is given:
// with none its Memory is a nil interface, so the proxy leaves memory off.
func TestAgentModelGetsMemoryOnlyWhenOn(t *testing.T) {
	key, err := secrets.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	var r provider.Registry
	r.Register("openai", func(provider.Config) (provider.Provider, error) { return aitest.New(), nil })
	cfg := config.AICfg{Enabled: true, Provider: "openai", Model: "m"}
	live := aiSettings(cfg, "a-key", storetest.New(t), key, &r)
	svc := Memory(config.MemoryCfg{Enabled: true, URL: "http://127.0.0.1:1"}, "k")
	if svc == nil {
		t.Fatal("Memory with memory on = nil")
	}
	tests := []struct {
		name       string
		mem        agentproxy.Memory
		cache      *agentproxy.RecallCache
		wantMemory bool
	}{
		{"no memory leaves the proxy without memory or recall", nil, nil, false},
		{"a memory service gives the proxy memory, recall and the timeout", svc, agentproxy.NewRecallCache(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := AgentModel(context.Background(), cfg, live, tc.mem, tc.cache, 2*time.Second)
			if p == nil {
				t.Fatal("AgentModel = nil")
			}
			if (p.Memory != nil) != tc.wantMemory || (p.Recall != nil) != tc.wantMemory {
				t.Fatalf("Memory set %v, Recall set %v, want both %v", p.Memory != nil, p.Recall != nil, tc.wantMemory)
			}
			if tc.wantMemory && p.RecallTimeout != 2*time.Second {
				t.Fatalf("RecallTimeout = %v, want 2s", p.RecallTimeout)
			}
		})
	}
}
