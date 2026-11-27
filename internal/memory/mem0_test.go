package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testKey = "sk-mem0-secret-key"
	idA     = "0b8f7c3e-1d2a-4f5b-9c6d-7e8f9a0b1c2d"
)

// recorded is one request the fake mem0 saw.
type recorded struct {
	Method, Path, Query string
	Body                string
	Header              http.Header
}

// fakeMem0 answers like the mem0 v2.2.1 server (server/main.py and
// mem0/memory/main.py) behind the mem0 pack's allowlist
// (image/mem0pack/allowlist.py): list answers wrap in {"results": [...]},
// a memory's text is "memory", user_id and agent_id appear only when set,
// add answers carry id, memory and event, and GET /memories/{id} of an
// unknown id is 200 with null. Every route needs X-API-Key. It records each
// request.
type fakeMem0 struct {
	*httptest.Server
	mu   sync.Mutex
	seen []recorded
}

func newFakeMem0(t *testing.T) *fakeMem0 {
	t.Helper()
	f := &fakeMem0{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /memories", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"id":"` + idA + `","memory":"likes tea","event":"ADD"}]}`))
	})
	mux.HandleFunc("POST /search", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"id":"` + idA + `","memory":"likes tea","user_id":"alice","agent_id":"helper","score":0.87,` +
			`"created_at":"2026-03-04T05:06:07.123456-08:00","updated_at":"2026-03-05T05:06:07"},` +
			`{"id":"x","memory":"no stamps","created_at":"garbage"}]}`))
	})
	mux.HandleFunc("GET /memories", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"id":"` + idA + `","memory":"likes tea","user_id":"alice"}]}`))
	})
	mux.HandleFunc("GET /memories/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != idA {
			_, _ = w.Write([]byte("null"))
			return
		}
		_, _ = w.Write([]byte(`{"id":"` + idA + `","memory":"likes tea","user_id":"alice","created_at":"2026-03-04T05:06:07Z"}`))
	})
	mux.HandleFunc("DELETE /memories/{id}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":"Memory deleted successfully"}`))
	})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		f.mu.Unlock()
		if r.Header.Get("X-API-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(b)))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMem0) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.seen...)
}

func (f *fakeMem0) client() *Mem0 { return NewMem0(f.URL, testKey, f.Client()) }

// jsonOf decodes a recorded body for comparison without caring about key order.
func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("body %q is not a JSON object: %v", s, err)
	}
	return m
}

func TestMem0EveryRequestCarriesTheKeyAndNoOtherAuth(t *testing.T) {
	f := newFakeMem0(t)
	c := f.client()
	ctx := context.Background()
	_, _ = c.Add(ctx, "alice", "", []Message{{"user", "hi"}})
	_, _ = c.Search(ctx, "alice", "", "tea", 3)
	_, _ = c.List(ctx, "alice")
	_, _ = c.Get(ctx, idA)
	_ = c.Delete(ctx, idA)
	reqs := f.requests()
	if len(reqs) != 5 {
		t.Fatalf("got %d requests, want 5", len(reqs))
	}
	for _, r := range reqs {
		if got := r.Header.Get("X-API-Key"); got != testKey {
			t.Errorf("%s %s: X-API-Key = %q", r.Method, r.Path, got)
		}
		for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if r.Header.Get(h) != "" {
				t.Errorf("%s %s: sent %s", r.Method, r.Path, h)
			}
		}
	}
}

func TestMem0AddSendsTheScopeAtTopLevelAndReturnsWhatWasStored(t *testing.T) {
	f := newFakeMem0(t)
	got, err := f.client().Add(context.Background(), "alice", "helper", []Message{{"user", "I like tea"}, {"assistant", "noted"}})
	if err != nil {
		t.Fatal(err)
	}
	want := Memory{ID: idA, Text: "likes tea", UserID: "alice", AgentID: "helper"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Add = %+v, want [%+v]", got, want)
	}
	r := f.requests()[0]
	if r.Method != "POST" || r.Path != "/memories" {
		t.Fatalf("sent %s %s", r.Method, r.Path)
	}
	body := jsonOf(t, r.Body)
	if _, has := body["filters"]; has {
		t.Errorf("add body carries filters: %s", r.Body)
	}
	wantBody := `{"messages":[{"role":"user","content":"I like tea"},{"role":"assistant","content":"noted"}],"user_id":"alice","agent_id":"helper"}`
	if !jsonEqual(t, r.Body, wantBody) {
		t.Errorf("body = %s, want %s", r.Body, wantBody)
	}
}

func TestMem0AddWithoutAnAgentOmitsAgentID(t *testing.T) {
	f := newFakeMem0(t)
	if _, err := f.client().Add(context.Background(), "alice", "", []Message{{"user", "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, has := jsonOf(t, f.requests()[0].Body)["agent_id"]; has {
		t.Errorf("body has agent_id: %s", f.requests()[0].Body)
	}
}

func TestMem0AddThatStoredNothingIsSuccessWithNoMemories(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	got, err := NewMem0(srv.URL, testKey, srv.Client()).Add(context.Background(), "alice", "", []Message{{"user", "hello"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("Add = %+v, %v; want no memories and no error", got, err)
	}
}

func TestMem0AddUsesTheAddClientAndTheOthersUseTheDefault(t *testing.T) {
	f := newFakeMem0(t)
	var viaAdd, viaDefault int
	count := func(n *int) *http.Client {
		return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			*n++
			return f.Client().Transport.RoundTrip(r)
		})}
	}
	c := NewMem0(f.URL, testKey, count(&viaDefault), WithAddClient(count(&viaAdd)))
	ctx := context.Background()
	_, _ = c.Add(ctx, "alice", "", []Message{{"user", "hi"}})
	_, _ = c.Search(ctx, "alice", "", "tea", 1)
	_, _ = c.List(ctx, "alice")
	_, _ = c.Get(ctx, idA)
	_ = c.Delete(ctx, idA)
	if viaAdd != 1 || viaDefault != 4 {
		t.Fatalf("add client sent %d, default client sent %d; want 1 and 4", viaAdd, viaDefault)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	x, _ := json.Marshal(jsonOf(t, a))
	y, _ := json.Marshal(jsonOf(t, b))
	return string(x) == string(y)
}

func TestMem0SearchScopesByFiltersAndMapsTheAnswer(t *testing.T) {
	f := newFakeMem0(t)
	got, err := f.client().Search(context.Background(), "alice", "helper", "tea", 5)
	if err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	want := `{"query":"tea","filters":{"user_id":"alice","agent_id":"helper"},"top_k":5}`
	if r.Method != "POST" || r.Path != "/search" || !jsonEqual(t, r.Body, want) {
		t.Fatalf("sent %s %s %s, want POST /search %s", r.Method, r.Path, r.Body, want)
	}
	if len(got) != 2 {
		t.Fatalf("got %d memories, want 2", len(got))
	}
	first := Memory{
		ID: idA, Text: "likes tea", UserID: "alice", AgentID: "helper", Score: 0.87,
		CreatedAt: time.Date(2026, 3, 4, 5, 6, 7, 123456000, time.FixedZone("", -8*3600)),
		UpdatedAt: time.Date(2026, 3, 5, 5, 6, 7, 0, time.UTC),
	}
	if g := got[0]; g.ID != first.ID || g.Text != first.Text || g.UserID != first.UserID || g.AgentID != first.AgentID ||
		g.Score != first.Score || !g.CreatedAt.Equal(first.CreatedAt) || !g.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("first = %+v, want %+v", g, first)
	}
	if g := got[1]; g.UserID != "" || g.AgentID != "" || !g.CreatedAt.IsZero() || !g.UpdatedAt.IsZero() {
		t.Errorf("second = %+v, want empty scope and zero times for an unreadable stamp", g)
	}
}

func TestMem0SearchWithoutAnAgentOmitsAgentID(t *testing.T) {
	f := newFakeMem0(t)
	if _, err := f.client().Search(context.Background(), "alice", "", "tea", 5); err != nil {
		t.Fatal(err)
	}
	want := `{"query":"tea","filters":{"user_id":"alice"},"top_k":5}`
	if body := f.requests()[0].Body; !jsonEqual(t, body, want) {
		t.Errorf("body = %s, want %s", body, want)
	}
}

func TestMem0ListAsksForTheOwnersMemoriesUpToTheCap(t *testing.T) {
	f := newFakeMem0(t)
	got, err := f.client().List(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	if r.Method != "GET" || r.Path != "/memories" || r.Query != "top_k=1000&user_id=alice" {
		t.Errorf("sent %s %s?%s", r.Method, r.Path, r.Query)
	}
	if len(got) != 1 || got[0].ID != idA || got[0].UserID != "alice" {
		t.Errorf("List = %+v", got)
	}
}

func TestMem0ListEncodesTheOwnerInTheQuery(t *testing.T) {
	f := newFakeMem0(t)
	if _, err := f.client().List(context.Background(), "a&user_id=bob"); err != nil {
		t.Fatal(err)
	}
	if q := f.requests()[0].Query; q != "top_k=1000&user_id=a%26user_id%3Dbob" {
		t.Errorf("query = %q", q)
	}
}

func TestMem0GetMapsAMemoryAndTreatsNullAsNotFound(t *testing.T) {
	f := newFakeMem0(t)
	c := f.client()
	got, err := c.Get(context.Background(), idA)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != idA || got.Text != "likes tea" || got.UserID != "alice" || got.CreatedAt.IsZero() {
		t.Errorf("Get = %+v", got)
	}
	_, err = c.Get(context.Background(), "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestMem0DeleteSendsDELETEForTheID(t *testing.T) {
	f := newFakeMem0(t)
	if err := f.client().Delete(context.Background(), idA); err != nil {
		t.Fatal(err)
	}
	r := f.requests()[0]
	if r.Method != "DELETE" || r.Path != "/memories/"+idA {
		t.Errorf("sent %s %s", r.Method, r.Path)
	}
}

func TestMem0AnIDThatIsNotALowercaseUUIDIsNotFoundWithoutARequest(t *testing.T) {
	for _, id := range []string{"", "x", "../search", idA + "/history", strings.ToUpper(idA), idA + "?a=b", idA + " "} {
		t.Run(id, func(t *testing.T) {
			f := newFakeMem0(t)
			c := f.client()
			if _, err := c.Get(context.Background(), id); !errors.Is(err, ErrNotFound) {
				t.Errorf("Get err = %v, want ErrNotFound", err)
			}
			if err := c.Delete(context.Background(), id); !errors.Is(err, ErrNotFound) {
				t.Errorf("Delete err = %v, want ErrNotFound", err)
			}
			if n := len(f.requests()); n != 0 {
				t.Errorf("made %d requests, want none", n)
			}
		})
	}
}

func TestMem0RefusesAnOwnerOrLimitThatNamesNoOnePersonWithoutARequest(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{"user", "hi"}}
	calls := map[string]func(c *Mem0, owner string) error{
		"Add":    func(c *Mem0, o string) error { _, err := c.Add(ctx, o, "", msgs); return err },
		"Search": func(c *Mem0, o string) error { _, err := c.Search(ctx, o, "", "q", 5); return err },
		"List":   func(c *Mem0, o string) error { _, err := c.List(ctx, o); return err },
	}
	for name, call := range calls {
		for _, owner := range []string{"", "*", "  ", "\t\n"} {
			t.Run(name+" owner "+strconv.Quote(owner), func(t *testing.T) {
				f := newFakeMem0(t)
				if err := call(f.client(), owner); !errors.Is(err, ErrInvalid) {
					t.Errorf("err = %v, want ErrInvalid", err)
				}
				if n := len(f.requests()); n != 0 {
					t.Errorf("made %d requests, want none", n)
				}
			})
		}
	}
	for _, limit := range []int{0, -1} {
		t.Run("Search limit "+strconv.Itoa(limit), func(t *testing.T) {
			f := newFakeMem0(t)
			if _, err := f.client().Search(ctx, "alice", "", "q", limit); !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
			if n := len(f.requests()); n != 0 {
				t.Errorf("made %d requests, want none", n)
			}
		})
	}
}

func TestMem0AnswersMapToErrorsThatNeverCarryTheKeyOrTheBody(t *testing.T) {
	const body = "detail with sk-mem0-secret-key inside"
	tests := []struct {
		name     string
		status   int
		body     string
		sentinel error
		contains []string
	}{
		{name: "401 is refused", status: 401, sentinel: ErrRefused},
		{name: "404 on a search names method path and status", status: 404, contains: []string{"POST", "/search", "404"}},
		{name: "400 names method path and status", status: 400, body: body, contains: []string{"POST", "/search", "400"}},
		{name: "500 names method path and status", status: 500, body: body, contains: []string{"POST", "/search", "500"}},
		{name: "an oversized body errors", status: 200, body: strings.Repeat("a", maxBody+1), contains: []string{"POST", "/search", "larger than"}},
		{name: "a body that is not a list errors", status: 200, body: `"nope"`, contains: []string{"mem0:"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := NewMem0(srv.URL, testKey, srv.Client()).Search(context.Background(), "alice", "", "q", 5)
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.sentinel != nil && !errors.Is(err, tc.sentinel) {
				t.Errorf("err = %v, want %v", err, tc.sentinel)
			}
			for _, s := range tc.contains {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("err %q lacks %q", err, s)
				}
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "detail with") {
				t.Errorf("err %q leaks the key or the body", err)
			}
		})
	}
}

func TestMem0ATransportFailureNamesNoKeyAndNoQuery(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := NewMem0(url, testKey, &http.Client{Timeout: time.Second})
	for name, call := range map[string]func() error{
		"Add":    func() error { _, err := c.Add(context.Background(), "alice", "", []Message{{"user", "x"}}); return err },
		"Search": func() error { _, err := c.Search(context.Background(), "alice", "", "q", 1); return err },
		"List":   func() error { _, err := c.List(context.Background(), "alice"); return err },
		"Get":    func() error { _, err := c.Get(context.Background(), idA); return err },
		"Delete": func() error { return c.Delete(context.Background(), idA) },
	} {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "user_id") {
				t.Errorf("err %q leaks the key or the query", err)
			}
			if !strings.HasPrefix(err.Error(), "mem0: ") {
				t.Errorf("err %q lacks the mem0: prefix", err)
			}
		})
	}
}

func TestMem0AWrongKeyIsRefusedOnEveryCall(t *testing.T) {
	f := newFakeMem0(t)
	c := NewMem0(f.URL, "wrong", f.Client())
	ctx := context.Background()
	if _, err := c.Add(ctx, "alice", "", []Message{{"user", "x"}}); !errors.Is(err, ErrRefused) {
		t.Errorf("Add err = %v", err)
	}
	if _, err := c.List(ctx, "alice"); !errors.Is(err, ErrRefused) {
		t.Errorf("List err = %v", err)
	}
	if err := c.Delete(ctx, idA); !errors.Is(err, ErrRefused) {
		t.Errorf("Delete err = %v", err)
	}
}

func TestMem0RefusesANameMem0WouldReadAsSomeoneElseWithoutARequest(t *testing.T) {
	ctx := context.Background()
	msgs := []Message{{"user", "hi"}}
	calls := map[string]func(c *Mem0, owner, agent string) error{
		"Add":    func(c *Mem0, o, a string) error { _, err := c.Add(ctx, o, a, msgs); return err },
		"Search": func(c *Mem0, o, a string) error { _, err := c.Search(ctx, o, a, "q", 5); return err },
		"List":   func(c *Mem0, o, _ string) error { _, err := c.List(ctx, o); return err },
	}
	for name, call := range calls {
		for _, bad := range []string{" alice", "alice ", "\talice", "*", "\x1falice", "alice\x1c", "\x00alice", "\u00a0alice", "alice\u2003"} {
			t.Run(name+" owner "+strconv.Quote(bad), func(t *testing.T) {
				f := newFakeMem0(t)
				if err := call(f.client(), bad, ""); !errors.Is(err, ErrInvalid) {
					t.Errorf("err = %v, want ErrInvalid", err)
				}
				if n := len(f.requests()); n != 0 {
					t.Errorf("made %d requests, want none", n)
				}
			})
			if name == "List" {
				continue
			}
			t.Run(name+" agent "+strconv.Quote(bad), func(t *testing.T) {
				f := newFakeMem0(t)
				if err := call(f.client(), "alice", bad); !errors.Is(err, ErrInvalid) {
					t.Errorf("err = %v, want ErrInvalid", err)
				}
				if n := len(f.requests()); n != 0 {
					t.Errorf("made %d requests, want none", n)
				}
			})
		}
	}
}

func TestMem0DoesNotFollowRedirectsSoTheKeyStaysAtItsAddress(t *testing.T) {
	other := newFakeMem0(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()
	mine := &http.Client{}
	c := NewMem0(redirector.URL, testKey, mine, WithAddClient(&http.Client{}))
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"Add":    func() error { _, err := c.Add(ctx, "alice", "", []Message{{"user", "x"}}); return err },
		"Search": func() error { _, err := c.Search(ctx, "alice", "", "q", 1); return err },
		"List":   func() error { _, err := c.List(ctx, "alice"); return err },
		"Get":    func() error { _, err := c.Get(ctx, idA); return err },
		"Delete": func() error { return c.Delete(ctx, idA) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Error("want an error for a redirect")
			}
		})
	}
	if n := len(other.requests()); n != 0 {
		t.Errorf("the redirect target got %d requests, want none", n)
	}
	if mine.CheckRedirect != nil {
		t.Error("the caller's client was changed")
	}
}

func TestMem0WithANilClientUsesATenSecondTimeout(t *testing.T) {
	c := NewMem0("http://mem0.invalid", testKey, nil, WithAddClient(nil))
	if c.client.Timeout != 10*time.Second || c.addClient.Timeout != 10*time.Second {
		t.Errorf("timeouts = %v, %v; want 10s", c.client.Timeout, c.addClient.Timeout)
	}
}

// A 404 means "no such memory" only for a get or delete by id. The mem0
// pack's allowlist answers 404 for any route it does not allow, so for
// search, list and add it is a wrong address or a changed pack, and must not
// look like an empty result.
func TestMem0A404IsNotFoundOnlyForByIDCalls(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := NewMem0(srv.URL, testKey, srv.Client())
	ctx := context.Background()
	tests := []struct {
		name         string
		run          func() error
		wantNotFound bool
		contains     []string
	}{
		{"get", func() error { _, err := c.Get(ctx, idA); return err }, true, nil},
		{"delete", func() error { return c.Delete(ctx, idA) }, true, nil},
		{"search", func() error { _, err := c.Search(ctx, "alice", "", "q", 5); return err }, false, []string{"POST", "/search", "404"}},
		{"list", func() error { _, err := c.List(ctx, "alice"); return err }, false, []string{"GET", "/memories", "404"}},
		{"add", func() error {
			_, err := c.Add(ctx, "alice", "", []Message{{Role: "user", Text: "hi"}})
			return err
		}, false, []string{"POST", "/memories", "404"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrNotFound); got != tt.wantNotFound {
				t.Errorf("errors.Is(ErrNotFound) = %v, want %v (err %v)", got, tt.wantNotFound, err)
			}
			for _, s := range tt.contains {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("err %q lacks %q", err, s)
				}
			}
		})
	}
}

func TestRememberStoresTheTextAsGivenWithoutExtraction(t *testing.T) {
	f := newFakeMem0(t)
	c := NewMem0(f.URL, testKey, f.Client(), WithInstructions("x"))
	got, err := c.Remember(context.Background(), "alice", "I prefer tabs over spaces")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UserID != "alice" {
		t.Fatalf("Remember = %+v, want one memory for alice", got)
	}
	r := f.requests()[0]
	if r.Method != "POST" || r.Path != "/memories" {
		t.Fatalf("sent %s %s", r.Method, r.Path)
	}
	if r.Header.Get("X-API-Key") != testKey {
		t.Errorf("X-API-Key = %q", r.Header.Get("X-API-Key"))
	}
	body := jsonOf(t, r.Body)
	if _, has := body["prompt"]; has {
		t.Errorf("remember body carries prompt: %s", r.Body)
	}
	want := `{"messages":[{"role":"user","content":"I prefer tabs over spaces"}],"user_id":"alice","infer":false}`
	if !jsonEqual(t, r.Body, want) {
		t.Errorf("body = %s, want %s", r.Body, want)
	}
}

func TestRememberRefusesAnOwnerThatIsNotOnePersonWithoutARequest(t *testing.T) {
	for _, owner := range []string{"", "*", " alice", "alice\n", "   "} {
		t.Run("owner "+owner, func(t *testing.T) {
			f := newFakeMem0(t)
			if _, err := f.client().Remember(context.Background(), owner, "hi"); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			if n := len(f.requests()); n != 0 {
				t.Errorf("sent %d requests, want none", n)
			}
		})
	}
}

func TestAddSendsTheExtractionInstructionsOnlyWhenSet(t *testing.T) {
	tests := []struct {
		name   string
		opts   []Option
		want   string
		wantIn bool
	}{
		{"with instructions", []Option{WithInstructions("keep only lasting facts")}, "keep only lasting facts", true},
		{"without instructions", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeMem0(t)
			c := NewMem0(f.URL, testKey, f.Client(), tt.opts...)
			if _, err := c.Add(context.Background(), "alice", "", []Message{{"user", "hi"}}); err != nil {
				t.Fatal(err)
			}
			got, has := jsonOf(t, f.requests()[0].Body)["prompt"]
			if has != tt.wantIn || (has && got != tt.want) {
				t.Errorf("prompt = %v (present %v), want %q (present %v)", got, has, tt.want, tt.wantIn)
			}
		})
	}
}
