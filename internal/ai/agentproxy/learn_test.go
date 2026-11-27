package agentproxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/ai/agentproxy"
	"github.com/nebari-dev/javo/internal/memory"
)

// addCall is one Add the learning fake saw.
type addCall struct {
	owner, agent string
	msgs         []memory.Message
}

// rememberCall is one Remember the learning fake saw.
type rememberCall struct{ owner, text string }

// learnMemory records Add and Remember calls and answers them with err. When
// release is set, Add signals started and waits for release to close. Search
// finds nothing. It is safe for concurrent use.
type learnMemory struct {
	mu        sync.Mutex
	err       error
	panicWith any // when set, Add and Remember record the call, then panic with it
	started   chan struct{}
	release   chan struct{}
	adds      []addCall
	remembers []rememberCall
}

func (f *learnMemory) Search(context.Context, string, string, string, int) ([]memory.Memory, error) {
	return nil, nil
}

func (f *learnMemory) Add(ctx context.Context, owner, agent string, msgs []memory.Message) ([]memory.Memory, error) {
	f.mu.Lock()
	f.adds = append(f.adds, addCall{owner, agent, append([]memory.Message(nil), msgs...)})
	f.mu.Unlock()
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	if f.release != nil {
		f.started <- struct{}{}
		<-f.release
	}
	if f.err != nil {
		return nil, f.err
	}
	return []memory.Memory{{ID: "1"}, {ID: "2"}}, nil
}

func (f *learnMemory) Remember(ctx context.Context, owner, text string) ([]memory.Memory, error) {
	f.mu.Lock()
	f.remembers = append(f.remembers, rememberCall{owner, text})
	f.mu.Unlock()
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	if f.err != nil {
		return nil, f.err
	}
	return []memory.Memory{{ID: "1"}}, nil
}

func (f *learnMemory) calls() ([]addCall, []rememberCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]addCall(nil), f.adds...), append([]rememberCall(nil), f.remembers...)
}

// scripted is an upstream that answers every call with one status, content
// type and body.
func scripted(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

// events joins data lines into a server-sent event stream.
func events(lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: " + l + "\n\n")
	}
	return b.String()
}

const usageEvent = `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`

func notedStream() string {
	return events(`{"choices":[{"delta":{"role":"assistant","content":"No"}}]}`, `{"choices":[{"delta":{"content":"ted."}}]}`, usageEvent, `[DONE]`)
}

const notedJSON = `{"choices":[{"message":{"role":"assistant","content":"Noted."}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`

// learnProxy returns a proxy on s with mem, and its handler.
func learnProxy(s *httptest.Server, mem agentproxy.Memory) (*agentproxy.Proxy, http.Handler) {
	up := agentproxy.Upstream{BaseURL: s.URL + "/v1", APIKey: adminKey, Model: "admin/model"}
	p := &agentproxy.Proxy{
		Upstream:        func(context.Context) (agentproxy.Upstream, bool) { return up, true },
		MaxRequestBytes: 1 << 20,
		Caller:          func(context.Context) (string, bool) { return "alice", true },
		Memory:          mem,
		RecallTimeout:   time.Second,
	}
	mux := http.NewServeMux()
	p.Register(mux)
	return p, mux
}

// userTurn is a request body whose only message is the person's text.
func userTurn(text string, stream bool) string {
	s := ""
	if stream {
		s = `,"stream":true`
	}
	return `{"messages":[{"role":"user","content":` + quote(text) + `}]` + s + `}`
}

func quote(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func TestRememberThatStoresTheSentenceVerbatim(t *testing.T) {
	mem := &learnMemory{}
	p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
	post(h, userTurn("remember that I prefer tabs over spaces", false))
	agentproxy.WaitLearning(p)

	adds, rems := mem.calls()
	want := []rememberCall{{"alice", "I prefer tabs over spaces"}}
	if !reflect.DeepEqual(rems, want) {
		t.Fatalf("remembers %+v, want %+v", rems, want)
	}
	if len(adds) != 0 {
		t.Fatalf("an explicit turn was also learned automatically: %+v", adds)
	}
}

func TestLearnSendsTypedTurnsWithTheAnswer(t *testing.T) {
	long := strings.Repeat("é", 2000)
	cases := []struct {
		name        string
		contentType string
		body        string
		stream      bool
		answer      string
	}{
		{"streamed", "text/event-stream", notedStream(), true, "Noted."},
		{"not streamed", "application/json", notedJSON, false, "Noted."},
		{
			"a long streamed answer is cut to 4000 runes", "text/event-stream",
			events(`{"choices":[{"delta":{"content":"`+long+`"}}]}`, `{"choices":[{"delta":{"content":"`+long+`"}}]}`, `{"choices":[{"delta":{"content":"`+long+`"}}]}`, `[DONE]`),
			true, strings.Repeat("é", 4000),
		},
		{
			"a long answer that is not streamed is cut to 4000 runes", "application/json",
			`{"choices":[{"message":{"content":"` + long + long + long + `"}}]}`,
			false, strings.Repeat("é", 4000),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			mem := &learnMemory{}
			p, h := learnProxy(scripted(t, 200, tc.contentType, tc.body), mem)
			w := post(h, userTurn("  I use Go 1.25 at work  ", tc.stream))
			agentproxy.WaitLearning(p)

			if w.Body.String() != tc.body {
				t.Fatalf("the agent got\n%q\nwant the upstream's bytes\n%q", w.Body, tc.body)
			}
			adds, rems := mem.calls()
			want := []addCall{{"alice", "", []memory.Message{{Role: "user", Text: "I use Go 1.25 at work"}, {Role: "assistant", Text: tc.answer}}}}
			if !reflect.DeepEqual(adds, want) {
				t.Fatalf("adds %+v, want %+v", adds, want)
			}
			if len(rems) != 0 {
				t.Fatalf("remembers %+v, want none", rems)
			}
			if !strings.Contains(logs.String(), "memory.learned user=alice count=2") {
				t.Fatalf("no memory.learned line: %s", logs)
			}
		})
	}
}

func TestLearnNeverRunsOnLoopStepsOrHousekeeping(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"a tool result is the last message", `{"messages":[{"role":"user","content":"fix the bug"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"main.go"}]}`},
		{"pi summarization", `{"messages":[{"role":"system","content":"You are a context summarization assistant. Summarize."},{"role":"user","content":"the whole conversation"}]}`},
		{"typed text over 8000 runes", userTurn(strings.Repeat("a", 8001), false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mem := &learnMemory{}
			p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
			if w := post(h, tc.body); w.Code != http.StatusOK {
				t.Fatalf("answer: %d %s", w.Code, w.Body)
			}
			agentproxy.WaitLearning(p)
			if adds, rems := mem.calls(); len(adds) != 0 || len(rems) != 0 {
				t.Fatalf("learned %+v %+v, want nothing", adds, rems)
			}
		})
	}
}

// brokenWriter is an agent that goes away after the first write.
type brokenWriter struct {
	*httptest.ResponseRecorder
	writes int
}

func (b *brokenWriter) Write(p []byte) (int, error) {
	b.writes++
	if b.writes > 1 {
		return 0, errors.New("client went away")
	}
	return b.ResponseRecorder.Write(p)
}

func TestLearnSkipsFailedAnswers(t *testing.T) {
	t.Run("upstream 500", func(t *testing.T) {
		mem := &learnMemory{}
		p, h := learnProxy(scripted(t, 500, "application/json", `{"error":{"message":"boom"}}`), mem)
		post(h, userTurn("I use Go 1.25 at work", false))
		agentproxy.WaitLearning(p)
		if adds, _ := mem.calls(); len(adds) != 0 {
			t.Fatalf("adds %+v, want none", adds)
		}
	})
	t.Run("client disconnects mid-stream", func(t *testing.T) {
		mem := &learnMemory{}
		p, h := learnProxy(scripted(t, 200, "text/event-stream", notedStream()), mem)
		r := httptest.NewRequest("POST", agentproxy.Route+"/chat/completions", strings.NewReader(userTurn("I use Go 1.25 at work", true)))
		r.Header.Set("Content-Type", "application/json")
		w := &brokenWriter{ResponseRecorder: httptest.NewRecorder()}
		h.ServeHTTP(w, r)
		agentproxy.WaitLearning(p)
		if adds, _ := mem.calls(); len(adds) != 0 {
			t.Fatalf("adds %+v, want none", adds)
		}
	})
	t.Run("client gone before a non-streamed answer", func(t *testing.T) {
		mem := &learnMemory{}
		p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
		r := httptest.NewRequest("POST", agentproxy.Route+"/chat/completions", strings.NewReader(userTurn("I use Go 1.25 at work", false)))
		r.Header.Set("Content-Type", "application/json")
		w := &brokenWriter{ResponseRecorder: httptest.NewRecorder(), writes: 1}
		h.ServeHTTP(w, r)
		agentproxy.WaitLearning(p)
		if adds, _ := mem.calls(); len(adds) != 0 {
			t.Fatalf("adds %+v, want none", adds)
		}
	})
}

func TestLearnWithNoAnswerTextSendsTheTypedMessageAlone(t *testing.T) {
	stream := events(
		`{"choices":[{"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"ls","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"content":null}}]}`,
		usageEvent, `[DONE]`)
	mem := &learnMemory{}
	p, h := learnProxy(scripted(t, 200, "text/event-stream", stream), mem)
	post(h, userTurn("list the files", true))
	agentproxy.WaitLearning(p)

	adds, _ := mem.calls()
	want := []addCall{{"alice", "", []memory.Message{{Role: "user", Text: "list the files"}}}}
	if !reflect.DeepEqual(adds, want) {
		t.Fatalf("adds %+v, want %+v", adds, want)
	}
}

func TestLearnRefusesSSNWithoutLoggingIt(t *testing.T) {
	cases := []struct{ name, text string }{
		{"automatic", "my ssn is 123-45-6789"},
		{"explicit", "remember that my ssn is 123-45-6789"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			mem := &learnMemory{err: memory.ErrWriteRefused}
			p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
			post(h, userTurn(tc.text, false))
			agentproxy.WaitLearning(p)

			if adds, rems := mem.calls(); len(adds)+len(rems) != 1 {
				t.Fatalf("adds %+v remembers %+v, want one write", adds, rems)
			}
			got := logs.String()
			if !strings.Contains(got, "memory.write_refused user=alice reason=ssn") {
				t.Fatalf("no memory.write_refused line with reason=ssn: %s", got)
			}
			if strings.Contains(got, "123-45-6789") || strings.Contains(got, tc.text) || strings.Contains(got, "Noted.") {
				t.Fatalf("an SSN, the typed text or the answer reached the log: %s", got)
			}
		})
	}
}

func TestLearnLogsOtherFailuresWithoutTheText(t *testing.T) {
	logs := captureLogs(t)
	mem := &learnMemory{err: errors.New("mem is down")}
	p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
	post(h, userTurn("SECRET-TYPED-TEXT", false))
	agentproxy.WaitLearning(p)

	got := logs.String()
	if !strings.Contains(got, "memory.learn_failed user=alice") || !strings.Contains(got, "mem is down") {
		t.Fatalf("no memory.learn_failed line: %s", got)
	}
	if strings.Contains(got, "SECRET-TYPED-TEXT") || strings.Contains(got, "Noted.") {
		t.Fatalf("the typed text or the answer reached the log: %s", got)
	}
}

func TestLearnRunsAtMostFourAtOnce(t *testing.T) {
	logs := captureLogs(t)
	mem := &learnMemory{started: make(chan struct{}, 5), release: make(chan struct{})}
	p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
	for i := 0; i < 4; i++ {
		post(h, userTurn("turn", false))
	}
	for i := 0; i < 4; i++ {
		select {
		case <-mem.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d Adds started, want 4", i)
		}
	}
	post(h, userTurn("turn", false))
	if n := strings.Count(logs.String(), "memory.learn_skipped user=alice reason=busy"); n != 1 {
		t.Fatalf("%d busy lines, want 1: %s", n, logs)
	}
	close(mem.release)
	agentproxy.WaitLearning(p)
	if adds, _ := mem.calls(); len(adds) != 4 {
		t.Fatalf("%d Adds, want 4", len(adds))
	}
}

func TestLearnSkipsOverlongExplicitText(t *testing.T) {
	logs := captureLogs(t)
	mem := &learnMemory{}
	p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
	post(h, userTurn("remember that "+strings.Repeat("ü", 1001), false))
	agentproxy.WaitLearning(p)

	adds, rems := mem.calls()
	if len(rems) != 0 || len(adds) != 0 {
		t.Fatalf("adds %+v remembers %+v, want none", adds, rems)
	}
	if !strings.Contains(logs.String(), "memory.learn_skipped user=alice reason=too_long") {
		t.Fatalf("no too_long line: %s", logs)
	}
}

func TestLearnSurvivesAPanicInTheMemoryService(t *testing.T) {
	cases := []struct{ name, typed, secret string }{
		{"automatic", "SECRET-TYPED-TEXT", "SECRET-TYPED-TEXT"},
		{"explicit", "remember that SECRET-TYPED-TEXT", "SECRET-TYPED-TEXT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			mem := &learnMemory{panicWith: "boom while storing " + tc.secret}
			p, h := learnProxy(scripted(t, 200, "application/json", notedJSON), mem)
			post(h, userTurn(tc.typed, false))
			agentproxy.WaitLearning(p)

			got := logs.String()
			if n := strings.Count(got, "memory.learn_panicked user=alice"); n != 1 {
				t.Fatalf("%d memory.learn_panicked lines, want 1: %s", n, got)
			}
			if strings.Contains(got, tc.secret) || strings.Contains(got, "boom while storing") {
				t.Fatalf("the typed text or the panic value reached the log: %s", got)
			}

			// The slot was released: more turns than there are slots all learn.
			for i := 0; i < 4; i++ {
				post(h, userTurn(tc.typed, false))
				agentproxy.WaitLearning(p)
			}
			got = logs.String()
			if strings.Contains(got, "reason=busy") {
				t.Fatalf("a learn slot was not released after the panic: %s", got)
			}
			if n := strings.Count(got, "memory.learn_panicked user=alice"); n != 5 {
				t.Fatalf("%d memory.learn_panicked lines, want 5: %s", n, got)
			}
		})
	}
}
