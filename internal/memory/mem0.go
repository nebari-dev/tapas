package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxBody caps what one mem0 answer may be; a list of 1000 memories is the
// largest.
const maxBody = 16 << 20

// listLimit is mem0 server's cap on top_k for GET /memories.
const listLimit = 1000

// memoryID is what mem0's ids look like, and what the mem0 pack's allowlist
// admits in a by-id path; anything else never reaches a URL.
var memoryID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Mem0 is a Client for the mem0 pack's REST subset: POST /memories, GET
// /memories, GET and DELETE /memories/{id}, and POST /search.
type Mem0 struct {
	base      string
	key       string
	client    *http.Client
	addClient *http.Client
}

var _ Client = (*Mem0)(nil)

// Option changes how NewMem0 builds a Mem0.
type Option func(*Mem0)

// WithAddClient sends Add through hc instead of the default client. An add
// runs a model to extract facts and takes far longer than the other calls, so
// it needs a longer timeout. A timed-out add may still have been stored, so
// callers must not retry it blindly.
func WithAddClient(hc *http.Client) Option {
	return func(m *Mem0) { m.addClient = noRedirects(hc) }
}

// NewMem0 returns a client for the mem0 pack at baseURL, sending apiKey as
// X-API-Key. hc carries every call except Add, which uses it too unless
// WithAddClient says otherwise.
func NewMem0(baseURL, apiKey string, hc *http.Client, opts ...Option) *Mem0 {
	m := &Mem0{base: strings.TrimSuffix(baseURL, "/"), key: apiKey, client: noRedirects(hc)}
	m.addClient = m.client
	for _, o := range opts {
		o(m)
	}
	return m
}

// noRedirects returns a copy of hc that does not follow redirects, so the key
// is never sent anywhere but the address it was configured for. The caller's
// client is left as it was.
func noRedirects(hc *http.Client) *http.Client {
	c := http.Client{}
	if hc != nil {
		c = *hc
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// checkOwner refuses, before any request, an owner that is not one person
// exactly as written: empty, the wildcard, or padded with whitespace. mem0
// strips whitespace from user_id before using it, so " alice" would be stored
// and read as "alice", another person's memories.
func checkOwner(owner string) error {
	if owner == "" || strings.TrimSpace(owner) == "" {
		return fmt.Errorf("mem0: %w: owner must be one person", ErrInvalid)
	}
	return checkScope("owner", owner)
}

// checkScope refuses a user or agent name mem0 would read as something else:
// the wildcard, or one with surrounding whitespace (mem0 trims it).
func checkScope(what, v string) error {
	if v == "*" || v != strings.TrimSpace(v) {
		return fmt.Errorf("mem0: %w: %s must be a plain name", ErrInvalid, what)
	}
	return nil
}

// checkAgent is checkScope for an agent, which may be empty (no agent).
func checkAgent(agent string) error {
	if agent == "" {
		return nil
	}
	return checkScope("agent", agent)
}

// wireMemory is one memory as mem0 v2.2.1 writes it: the text is "memory",
// user_id and agent_id appear only when set, and times are ISO 8601 strings.
type wireMemory struct {
	ID        string  `json:"id"`
	Memory    string  `json:"memory"`
	UserID    string  `json:"user_id"`
	AgentID   string  `json:"agent_id"`
	Score     float64 `json:"score"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func (w wireMemory) memory() Memory {
	return Memory{
		ID: w.ID, Text: w.Memory, UserID: w.UserID, AgentID: w.AgentID, Score: w.Score,
		CreatedAt: parseTime(w.CreatedAt), UpdatedAt: parseTime(w.UpdatedAt),
	}
}

// parseTime reads mem0's timestamps, which may or may not carry a zone. One
// that cannot be read is the zero time: a bad stamp must not hide a memory.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func memories(raw []byte) ([]Memory, error) {
	var out struct {
		Results []wireMemory `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("mem0: answer is not a list of memories")
	}
	got := make([]Memory, 0, len(out.Results))
	for _, w := range out.Results {
		got = append(got, w.memory())
	}
	return got, nil
}

// Search returns the owner's memories closest to query, at most limit. A
// non-empty agent narrows it to that agent's memories.
func (m *Mem0) Search(ctx context.Context, owner, agent, query string, limit int) ([]Memory, error) {
	if err := checkOwner(owner); err != nil {
		return nil, err
	}
	if err := checkAgent(agent); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("mem0: %w: limit must be above zero", ErrInvalid)
	}
	filters := map[string]string{"user_id": owner}
	if agent != "" {
		filters["agent_id"] = agent
	}
	raw, err := m.post(ctx, m.client, "/search", map[string]any{"query": query, "filters": filters, "top_k": limit})
	if err != nil {
		return nil, err
	}
	return memories(raw)
}

// Add has mem0 learn from msgs for the owner (and agent, when set) and
// returns the memories it stored; none when it found nothing worth keeping.
// The scope goes in top-level fields: the mem0 pack refuses an add with
// "filters".
func (m *Mem0) Add(ctx context.Context, owner, agent string, msgs []Message) ([]Memory, error) {
	if err := checkOwner(owner); err != nil {
		return nil, err
	}
	if err := checkAgent(agent); err != nil {
		return nil, err
	}
	type wireMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	wire := make([]wireMessage, 0, len(msgs))
	for _, msg := range msgs {
		wire = append(wire, wireMessage{Role: msg.Role, Content: msg.Text})
	}
	body := map[string]any{"messages": wire, "user_id": owner}
	if agent != "" {
		body["agent_id"] = agent
	}
	raw, err := m.post(ctx, m.addClient, "/memories", body)
	if err != nil {
		return nil, err
	}
	got, err := memories(raw)
	if err != nil {
		return nil, err
	}
	// mem0's add answers with id and text only; they were stored for these.
	for i := range got {
		got[i].UserID, got[i].AgentID = owner, agent
	}
	return got, nil
}

// List returns the owner's memories, up to mem0's cap of 1000.
func (m *Mem0) List(ctx context.Context, owner string) ([]Memory, error) {
	if err := checkOwner(owner); err != nil {
		return nil, err
	}
	q := url.Values{"user_id": {owner}, "top_k": {strconv.Itoa(listLimit)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/memories?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("mem0: %w", err)
	}
	raw, err := m.do(m.client, req)
	if err != nil {
		return nil, err
	}
	return memories(raw)
}

// Get returns the memory with id, whoever owns it: mem0 does not check, so
// Service does. An id mem0 does not know is ErrNotFound.
func (m *Mem0) Get(ctx context.Context, id string) (Memory, error) {
	if !memoryID.MatchString(id) {
		return Memory{}, ErrNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+"/memories/"+id, nil)
	if err != nil {
		return Memory{}, fmt.Errorf("mem0: %w", err)
	}
	raw, err := m.do(m.client, req)
	if err != nil {
		return Memory{}, err
	}
	// mem0 answers 200 with null for an id that does not exist.
	var w *wireMemory
	if err := json.Unmarshal(raw, &w); err != nil {
		return Memory{}, fmt.Errorf("mem0: GET /memories/{id}: answer is not a memory")
	}
	if w == nil {
		return Memory{}, ErrNotFound
	}
	return w.memory(), nil
}

// Delete removes the memory with id, whoever owns it: mem0 does not check, so
// Service does. mem0 answers a delete of an unknown id with 400, so this does
// not report ErrNotFound for it.
func (m *Mem0) Delete(ctx context.Context, id string) error {
	if !memoryID.MatchString(id) {
		return ErrNotFound
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, m.base+"/memories/"+id, nil)
	if err != nil {
		return fmt.Errorf("mem0: %w", err)
	}
	_, err = m.do(m.client, req)
	return err
}

func (m *Mem0) post(ctx context.Context, hc *http.Client, path string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("mem0: POST %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+path, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("mem0: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return m.do(hc, req)
}

// do sends req with the key and maps mem0's refusals onto the package errors.
// Error texts carry the method and path, never the key or an answer body.
func (m *Mem0) do(hc *http.Client, req *http.Request) ([]byte, error) {
	req.Header.Set("X-API-Key", m.key)
	resp, err := hc.Do(req)
	if err != nil {
		// The transport's error names the URL (its query holds a person's
		// name), so say only what failed.
		return nil, fmt.Errorf("mem0: %s %s: %w", req.Method, req.URL.Path, unwrapTransport(err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("mem0: %s %s: %w", req.Method, req.URL.Path, unwrapTransport(err))
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, ErrRefused
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("mem0: %s %s answered %d", req.Method, req.URL.Path, resp.StatusCode)
	case len(raw) > maxBody:
		return nil, fmt.Errorf("mem0: %s %s: answer larger than %d bytes", req.Method, req.URL.Path, maxBody)
	}
	return raw, nil
}

// unwrapTransport drops the *url.Error wrapper, which repeats the full URL,
// and keeps the cause (a timeout, a refused connection) for errors.Is.
func unwrapTransport(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}
