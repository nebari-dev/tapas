// Package agentproxy is the model endpoint coding-agent apps call: an
// OpenAI-compatible chat completions route that forwards each request to the
// admin's provider with the admin's key, on the admin's model. Spend is
// capped by the admin at the provider (such as an OpenRouter key limit), not
// here. The key stays in the gateway; the agent
// authenticates with its app's own API token. Prompts and answers pass
// through and are never logged.
package agentproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/memory"
)

// Route is where the endpoint is served; an agent's base URL is
// $JAVO_URL + Route.
const Route = "/api/ai/openai/v1"

// DefaultMaxOutput is the most output a request may ask for.
const DefaultMaxOutput = 32000

// maxAnswerBytes bounds an answer that is not streamed.
const maxAnswerBytes = 32 << 20

// Upstream is the admin's provider the requests go to.
type Upstream struct {
	// BaseURL is the provider's OpenAI-compatible base, such as
	// https://openrouter.ai/api/v1; empty is OpenAI's.
	BaseURL string
	APIKey  string
	// Model replaces any model a request names that is neither it nor one
	// of More. GET /models lists Model, then More.
	Model string
	More  []string
	// Client sends the requests; nil is the proxy's Client.
	Client *http.Client
}

func (u Upstream) allows(model string) bool {
	return model != "" && (model == u.Model || slices.Contains(u.More, model))
}

func (u Upstream) base() string {
	if u.BaseURL == "" {
		return "https://api.openai.com/v1"
	}
	return strings.TrimRight(u.BaseURL, "/")
}

// Proxy forwards agents' requests to the admin's provider.
type Proxy struct {
	// Upstream is read for every request, so an admin's change applies to
	// the next one; false while there is no agent model.
	Upstream func(ctx context.Context) (Upstream, bool)
	// MaxRequestBytes caps a request body.
	MaxRequestBytes int64
	// Caller is who the request comes from, and whether they may use the
	// endpoint at all.
	Caller func(ctx context.Context) (person string, ok bool)
	// Client sends the requests; nil is http.DefaultClient.
	Client *http.Client
	// Memory is the person's memory store; nil turns memory off, and requests
	// are forwarded exactly as without it.
	Memory Memory
	// Recall caches recall results so the steps of one agent tool loop do not
	// each search; nil searches every time.
	Recall *RecallCache
	// RecallTimeout is the longest a call waits for memories before going on
	// without them; zero or less is 2 seconds.
	RecallTimeout time.Duration

	learnSlotsOnce sync.Once
	learnSlots     chan struct{}
	// learning counts running learn goroutines, so tests can wait for them.
	learning sync.WaitGroup
}

// defaultRecallTimeout is used when Proxy.RecallTimeout is not set.
const defaultRecallTimeout = 2 * time.Second

// Memory is what the proxy needs from the memory store.
type Memory interface {
	Search(ctx context.Context, owner, agent, query string, limit int) ([]memory.Memory, error)
	Add(ctx context.Context, owner, agent string, msgs []memory.Message) ([]memory.Memory, error)
	Remember(ctx context.Context, owner, text string) ([]memory.Memory, error)
}

// Register adds the routes to mux.
func (p *Proxy) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+Route+"/chat/completions", p.chat)
	mux.HandleFunc("GET "+Route+"/models", p.models)
}

// fail answers in the OpenAI error shape, which agents show as it is.
func fail(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": kind}})
}

const notAllowed = "Only a coding-agent app's own token may use this model endpoint."

const noModel = "The platform has no model for coding agents now; ask your admin."

func (p *Proxy) models(w http.ResponseWriter, r *http.Request) {
	if _, ok := p.Caller(r.Context()); !ok {
		fail(w, http.StatusForbidden, "permission_denied", notAllowed)
		return
	}
	up, ok := p.Upstream(r.Context())
	if !ok {
		fail(w, http.StatusServiceUnavailable, "server_error", noModel)
		return
	}
	data := []map[string]any{}
	for _, m := range append([]string{up.Model}, up.More...) {
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "javo"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (p *Proxy) chat(w http.ResponseWriter, r *http.Request) {
	person, ok := p.Caller(r.Context())
	if !ok {
		fail(w, http.StatusForbidden, "permission_denied", notAllowed)
		return
	}
	up, ok := p.Upstream(r.Context())
	if !ok {
		fail(w, http.StatusServiceUnavailable, "server_error", noModel)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, p.MaxRequestBytes))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		fail(w, http.StatusRequestEntityTooLarge, "invalid_request_error", fmt.Sprintf("The request is larger than the %d bytes allowed.", p.MaxRequestBytes))
		return
	case err != nil:
		fail(w, http.StatusBadRequest, "invalid_request_error", "The request could not be read.")
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", "The request is not a JSON object.")
		return
	}
	body, model, stream := rewrite(body, up)
	// text, typed and skipLearning are for learning from the exchange; recall
	// below changes body only when it adds the note.
	text, typed, skipLearning := "", false, false
	if p.Memory != nil {
		var msgs []json.RawMessage
		if json.Unmarshal(body["messages"], &msgs) == nil && msgs != nil {
			text, typed = lastUserText(msgs)
			skipLearning = housekeeping(msgs, text)
			if text != "" && !skipLearning {
				if mems := p.recall(r.Context(), person, text); len(mems) > 0 {
					if with := withNote(msgs, mems); len(with) != len(msgs) || !sameFirst(with, msgs) {
						body["messages"], _ = json.Marshal(with)
					}
				}
			}
		}
	}
	// ans collects the answer text only when the exchange may be learned.
	var ans *answer
	if p.Memory != nil && typed && !skipLearning && text != "" {
		ans = &answer{}
	}
	out, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, up.base()+"/chat/completions", bytes.NewReader(out))
	if err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "The request could not be made.")
		return
	}
	// Only these headers go out: never the caller's token or cookies.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+up.APIKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := p.client(up).Do(req)
	if err != nil {
		// The error names the URL, never the key or the body.
		slog.Warn("agent model call failed", "user", person, "err", err)
		audit(person, model, 0, 0, "unavailable")
		fail(w, http.StatusBadGateway, "server_error", "The AI provider could not be reached.")
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	var in, outTok int
	var werr error
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		in, outTok, _, werr = copyEvents(w, resp.Body, ans)
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
		_, werr = w.Write(b)
		in, outTok, _ = usageOf(b)
		ans.addMessage(b)
	}
	result := "ok"
	if resp.StatusCode >= 400 {
		result = fmt.Sprintf("status %d", resp.StatusCode)
	}
	audit(person, model, in, outTok, result)
	if ans != nil && resp.StatusCode/100 == 2 && werr == nil {
		p.startLearning(r.Context(), person, text, ans.String())
	}
}

// forwarded are the OpenAI chat completions fields that go to the provider;
// any other field (n, a provider's routing or plugin fields) is dropped, so
// a request cannot ask for several answers or another model. n is left out,
// which is one answer.
var forwarded = []string{
	"messages", "model", "stream", "stream_options", "temperature", "top_p",
	"max_tokens", "max_completion_tokens", "stop", "tools", "tool_choice",
	"response_format", "presence_penalty", "frequency_penalty", "seed", "user",
	"parallel_tool_calls",
}

// tokenFields are the fields an output limit is asked for in.
var tokenFields = []string{"max_tokens", "max_completion_tokens"}

// rewrite builds the provider's request from in: only the forwarded
// fields, the admin's model unless in names one up allows, an output limit of
// at most DefaultMaxOutput (DefaultMaxOutput when in asks for none), and a
// streamed answer asked to report its usage (for the audit line).
func rewrite(in map[string]json.RawMessage, up Upstream) (out map[string]json.RawMessage, model string, stream bool) {
	out = map[string]json.RawMessage{}
	for _, f := range forwarded {
		if v, ok := in[f]; ok {
			out[f] = v
		}
	}
	_ = json.Unmarshal(out["model"], &model)
	if !up.allows(model) {
		model = up.Model
		out["model"], _ = json.Marshal(model)
	}
	limit := DefaultMaxOutput
	named := []string{}
	for _, f := range tokenFields {
		if _, ok := out[f]; !ok {
			continue
		}
		named = append(named, f)
		if n, ok := tokenLimit(out[f]); ok && n < limit {
			limit = n
		}
	}
	if len(named) == 0 {
		named = tokenFields[:1]
	}
	for _, f := range named {
		out[f], _ = json.Marshal(limit)
	}
	delete(out, "stream_options")
	if json.Unmarshal(out["stream"], &stream) == nil && stream {
		out["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return out, model, stream
}

// tokenLimit reads an output limit written as a JSON number in any form
// (500, 5e2, 500.0) or as a string holding one, rounded down; ok is false
// for anything else, or for a limit below one.
func tokenLimit(raw json.RawMessage) (n int, ok bool) {
	text := strings.TrimSpace(string(raw))
	var s string
	if json.Unmarshal(raw, &s) == nil {
		text = strings.TrimSpace(s)
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(f) || f < 1 {
		return 0, false
	}
	if f >= DefaultMaxOutput {
		return DefaultMaxOutput, true
	}
	return int(f), true
}

func (p *Proxy) client(up Upstream) *http.Client {
	if up.Client != nil {
		return up.Client
	}
	if p.Client != nil {
		return p.Client
	}
	return http.DefaultClient
}

// copyEvents passes a server-sent event stream through line by line,
// flushing each event, and reads the usage the last events report. When ans
// is not nil it collects the answer text the events carry. err is the first
// error writing to w, after which copying stops.
func copyEvents(w http.ResponseWriter, r io.Reader, ans *answer) (in, out int, found bool, err error) {
	f, _ := w.(http.Flusher)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = bytes.TrimSpace(data)
			if i, o, ok := usageOf(data); ok {
				in, out, found = i, o, true
			}
			ans.addDelta(data)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			return in, out, found, err
		}
		if len(line) == 0 && f != nil {
			f.Flush()
		}
	}
	if f != nil {
		f.Flush()
	}
	return in, out, found, nil
}

// usageOf reads the usage of an answer or an event.
func usageOf(b []byte) (in, out int, found bool) {
	var v struct {
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &v) != nil || v.Usage == nil {
		return 0, 0, false
	}
	return v.Usage.Prompt, v.Usage.Completion, true
}

// audit writes one line per call: who, the model, the tokens and the
// outcome. Never the prompt or the answer.
func audit(person, model string, in, out int, result string) {
	slog.Info("ai.call", "user", person, "feature", "agent", "model", model, "tokens_in", in, "tokens_out", out, "outcome", result)
}

// sameFirst reports whether both slices start with the same message, which
// withNote does only when it added nothing.
func sameFirst(a, b []json.RawMessage) bool {
	return len(a) > 0 && len(b) > 0 && bytes.Equal(a[0], b[0])
}

// recall returns the memories to show for the query: the cached result, else
// a search that waits at most the recall timeout. A failure is logged without
// the query and is not cached, so the call goes on without memories.
func (p *Proxy) recall(ctx context.Context, person, query string) []memory.Memory {
	if p.Recall != nil {
		if mems, ok := p.Recall.get(person, query); ok {
			return mems
		}
	}
	timeout := p.RecallTimeout
	if timeout <= 0 {
		timeout = defaultRecallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mems, err := p.Memory.Search(ctx, person, "", query, maxNoteMemories)
	if err != nil {
		slog.Warn("memory.recall_failed", "user", person, "err", err)
		return nil
	}
	if p.Recall != nil {
		p.Recall.put(person, query, mems)
	}
	return mems
}
