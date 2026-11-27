package agentproxy_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/memory"
)

// searchCall is one Search the fake saw.
type searchCall struct {
	owner, agent, query string
	limit               int
}

// fakeMemory records its calls and answers Search with configured results, an
// error, or after a delay. It is safe for concurrent use.
type fakeMemory struct {
	mu       sync.Mutex
	results  []memory.Memory
	err      error
	delay    time.Duration
	searches []searchCall
	others   int
}

func (f *fakeMemory) Search(ctx context.Context, owner, agent, query string, limit int) ([]memory.Memory, error) {
	f.mu.Lock()
	f.searches = append(f.searches, searchCall{owner, agent, query, limit})
	results, err, delay := f.results, f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return results, err
}

func (f *fakeMemory) Add(ctx context.Context, owner, agent string, msgs []memory.Message) ([]memory.Memory, error) {
	f.mu.Lock()
	f.others++
	f.mu.Unlock()
	return nil, nil
}

func (f *fakeMemory) Remember(ctx context.Context, owner, text string) ([]memory.Memory, error) {
	f.mu.Lock()
	f.others++
	f.mu.Unlock()
	return nil, nil
}

func (f *fakeMemory) seen() []searchCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]searchCall(nil), f.searches...)
}

func memProxy(u *upstream, mem agentproxy.Memory, cache *agentproxy.RecallCache, timeout time.Duration) http.Handler {
	up := agentproxy.Upstream{BaseURL: u.URL + "/v1", APIKey: adminKey, Model: "admin/model"}
	p := &agentproxy.Proxy{
		Upstream:        func(context.Context) (agentproxy.Upstream, bool) { return up, true },
		MaxRequestBytes: 1 << 20,
		Caller:          func(context.Context) (string, bool) { return "alice", true },
		Memory:          mem,
		Recall:          cache,
		RecallTimeout:   timeout,
	}
	mux := http.NewServeMux()
	p.Register(mux)
	// Each request waits for the learning it started, so no background log
	// line lands while a test reads its logs.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		agentproxy.WaitLearning(p)
	})
}

// firstMessage returns the first upstream message's role and content text.
func firstMessage(t *testing.T, body map[string]any) (role, content string) {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("upstream got no messages: %v", body)
	}
	m, _ := msgs[0].(map[string]any)
	role, _ = m["role"].(string)
	content, _ = m["content"].(string)
	return role, content
}

func TestRecallAddsTheMemoriesToTheFirstSystemMessage(t *testing.T) {
	u := newUpstream(t)
	fake := &fakeMemory{results: []memory.Memory{{ID: "1", Text: "I prefer tabs over spaces"}}}
	w := post(memProxy(u, fake, agentproxy.NewRecallCache(), time.Second),
		`{"messages":[{"role":"system","content":"You are a coding agent."},{"role":"user","content":"  how do I indent?  "}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	role, content := firstMessage(t, u.body)
	if role != "system" || !strings.HasPrefix(content, "You are a coding agent.") || !strings.Contains(content, "I prefer tabs over spaces") {
		t.Fatalf("first message %q %q does not carry the memory after the agent's prompt", role, content)
	}
	want := []searchCall{{owner: "alice", agent: "", query: "how do I indent?", limit: 5}}
	got := fake.seen()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("searches %+v, want %+v", got, want)
	}
}

func TestRecallRunsOnLoopStepsFromTheCache(t *testing.T) {
	u := newUpstream(t)
	fake := &fakeMemory{results: []memory.Memory{{ID: "1", Text: "I prefer tabs over spaces"}}}
	h := memProxy(u, fake, agentproxy.NewRecallCache(), time.Second)

	post(h, `{"messages":[{"role":"user","content":"fix the bug"}]}`)
	_, first := firstMessage(t, u.body)
	post(h, `{"messages":[{"role":"user","content":"fix the bug"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"main.go"}]}`)
	_, second := firstMessage(t, u.body)

	if n := len(fake.seen()); n != 1 {
		t.Fatalf("%d searches, want 1 (the loop step should hit the cache)", n)
	}
	for i, c := range []string{first, second} {
		if !strings.Contains(c, "I prefer tabs over spaces") {
			t.Fatalf("call %d has no note: %q", i+1, c)
		}
	}
}

func TestRecallFailsOpenWhenMemIsDownOrSlow(t *testing.T) {
	const query = "SECRET-RECALL-QUERY"
	cases := []struct {
		name string
		fake *fakeMemory
	}{
		{"down", &fakeMemory{err: errors.New("mem is down")}},
		{"slow", &fakeMemory{delay: 5 * time.Second, results: []memory.Memory{{Text: "late memory"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			u := newUpstream(t)
			start := time.Now()
			w := post(memProxy(u, tc.fake, agentproxy.NewRecallCache(), 50*time.Millisecond),
				`{"messages":[{"role":"user","content":"`+query+`"}]}`)
			if time.Since(start) > 2*time.Second {
				t.Fatal("the call waited far past the recall limit")
			}
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Hello") {
				t.Fatalf("answer: %d %s", w.Code, w.Body)
			}
			msgs, _ := u.body["messages"].([]any)
			if len(msgs) != 1 {
				t.Fatalf("upstream got %d messages, want the original 1: %v", len(msgs), msgs)
			}
			if !strings.Contains(logs.String(), "memory.recall_failed") {
				t.Fatalf("no memory.recall_failed line: %s", logs)
			}
			if strings.Contains(logs.String(), query) || strings.Contains(logs.String(), "late memory") {
				t.Fatalf("a query or memory reached the log: %s", logs)
			}
		})
	}
}

func TestRecallDoesNotCacheFailures(t *testing.T) {
	u := newUpstream(t)
	fake := &fakeMemory{err: errors.New("down")}
	h := memProxy(u, fake, agentproxy.NewRecallCache(), time.Second)
	body := `{"messages":[{"role":"user","content":"same question"}]}`
	post(h, body)
	post(h, body)
	if n := len(fake.seen()); n != 2 {
		t.Fatalf("%d searches, want 2 (a failure must not be cached)", n)
	}
}

func TestRecallWorksWithoutACacheAndWithAZeroTimeout(t *testing.T) {
	u := newUpstream(t)
	fake := &fakeMemory{results: []memory.Memory{{Text: "I prefer tabs over spaces"}}}
	post(memProxy(u, fake, nil, 0), `{"messages":[{"role":"user","content":"hello"}]}`)
	role, content := firstMessage(t, u.body)
	if role != "system" || !strings.Contains(content, "I prefer tabs over spaces") {
		t.Fatalf("no note with a nil cache and zero timeout: %q %q", role, content)
	}
}

func TestRecallSkipsHousekeepingAndMissingMessages(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"summarization", `{"messages":[{"role":"system","content":"You are a context summarization assistant. Summarize."},{"role":"user","content":"the whole conversation"}]}`},
		{"no messages", `{"model":"admin/model"}`},
		{"messages not an array", `{"messages":"hi"}`},
		{"empty text", `{"messages":[{"role":"user","content":"   "}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := newUpstream(t)
			fake := &fakeMemory{results: []memory.Memory{{Text: "I prefer tabs over spaces"}}}
			w := post(memProxy(u, fake, agentproxy.NewRecallCache(), time.Second), tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("answer: %d %s", w.Code, w.Body)
			}
			if n := len(fake.seen()); n != 0 {
				t.Fatalf("%d searches, want none", n)
			}
			if b, _ := json.Marshal(u.body); strings.Contains(string(b), "tabs over spaces") {
				t.Fatalf("a note was added: %s", b)
			}
		})
	}
}

func TestRecallAddsNothingWhenThereAreNoResults(t *testing.T) {
	u := newUpstream(t)
	fake := &fakeMemory{}
	post(memProxy(u, fake, agentproxy.NewRecallCache(), time.Second), `{"messages":[{"role":"user","content":"hello"}]}`)
	if msgs, _ := u.body["messages"].([]any); len(msgs) != 1 {
		t.Fatalf("upstream got %d messages, want 1", len(msgs))
	}
}

// rawUpstream keeps the exact bytes of the last request body.
func rawUpstream(t *testing.T, got *[]byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestMemoryOffForwardsTheSameBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// want is what the proxy sent before memory existed: only the forwarded
		// fields, the admin's model, the output limit, marshalled with sorted keys.
		want string
	}{
		{
			"plain",
			`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"model":"x","extra":1}`,
			`{"max_tokens":32000,"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"model":"admin/model"}`,
		},
		{
			"streamed",
			`{"messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":100}`,
			`{"max_tokens":100,"messages":[{"role":"user","content":"hi"}],"model":"admin/model","stream":true,"stream_options":{"include_usage":true}}`,
		},
		{
			"with tools",
			`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"a"}],"tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object"}}}],"tool_choice":"auto"}`,
			`{"max_tokens":32000,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"a"}],"model":"admin/model","tool_choice":"auto","tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object"}}}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var offBytes, emptyBytes []byte
			off := rawUpstream(t, &offBytes)
			empty := rawUpstream(t, &emptyBytes)
			mk := func(s *httptest.Server, mem agentproxy.Memory) http.Handler {
				return memProxy(&upstream{Server: s}, mem, agentproxy.NewRecallCache(), time.Second)
			}
			// A nil *fakeMemory would be a non-nil interface, so pass nil directly.
			post(mk(off, nil), tc.in)
			fake := &fakeMemory{}
			post(mk(empty, fake), tc.in)

			if string(offBytes) != tc.want {
				t.Fatalf("memory off sent\n%s\nwant\n%s", offBytes, tc.want)
			}
			if string(emptyBytes) != tc.want {
				t.Fatalf("memory on with no results sent\n%s\nwant\n%s", emptyBytes, tc.want)
			}
		})
	}
}
