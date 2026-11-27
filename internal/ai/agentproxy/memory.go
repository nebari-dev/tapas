package agentproxy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nebari-dev/javo/internal/memory"
)

// piSummarizationPrompt is the start of pi-coding-agent 1.4.0's fixed
// summarization system prompt (dist/core/compaction/utils.js:145). A call whose
// first system message starts with it is housekeeping, not something the person
// typed. Re-check on a pi upgrade.
const piSummarizationPrompt = "You are a context summarization assistant."

const (
	// housekeepingRunes is the longest typed text that still counts as a message
	// the person wrote; longer text is treated as a paste or a summary call.
	housekeepingRunes = 8000
	maxNoteMemories   = 5
	maxMemoryRunes    = 500
	maxNoteRunes      = 2500
	// maxAnswerRunes is the most answer text learned along with a typed turn.
	maxAnswerRunes = 4000
	// maxRememberRunes is the longest "remember that" sentence that is stored.
	maxRememberRunes = 1000
	// learnSlots is how many learn calls may run at once.
	learnSlots = 4
)

const noteHeader = "What this person has told you in earlier conversations (from Tapas memory). Use it when it is relevant; do not mention this note."

var rememberRE = regexp.MustCompile(`(?is)^\s*remember(?:\s+that|:)\s+(.+)$`)

// messageText returns a chat message's role and its text: a string content as
// is, or the text of each text part joined with newlines (other parts, such as
// images, are skipped).
func messageText(raw json.RawMessage) (role, text string) {
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "", ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return m.Role, s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return m.Role, ""
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return m.Role, strings.Join(texts, "\n")
}

// lastUserText returns the trimmed text of the latest user message. typed is
// true when that message is also the last one, meaning the person just sent it
// rather than the agent continuing after a tool result.
func lastUserText(msgs []json.RawMessage) (text string, typed bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if role, t := messageText(msgs[i]); role == "user" {
			return strings.TrimSpace(t), i == len(msgs)-1
		}
	}
	return "", false
}

// housekeeping reports whether the call is the agent's own summarization or a
// very long paste, neither of which should be recalled for or learned from.
func housekeeping(msgs []json.RawMessage, userText string) bool {
	if len(msgs) > 0 {
		if role, text := messageText(msgs[0]); role == "system" && strings.HasPrefix(text, piSummarizationPrompt) {
			return true
		}
	}
	return utf8.RuneCountInString(userText) > housekeepingRunes
}

// explicitRemember returns what follows "remember that" or "remember:" at the
// start of the text. It is false when nothing follows.
func explicitRemember(text string) (string, bool) {
	m := rememberRE.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	got := strings.TrimSpace(m[1])
	return got, got != ""
}

// withNote returns a copy of msgs with the memory note added to the first
// message when it is a system message, or as a new system message at the front.
// The input slice and its messages are not changed.
func withNote(msgs []json.RawMessage, mems []memory.Memory) []json.RawMessage {
	lines := []string{noteHeader}
	total := 0
	for _, m := range mems {
		if len(lines)-1 >= maxNoteMemories {
			break
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		if r := []rune(text); len(r) > maxMemoryRunes {
			text = string(r[:maxMemoryRunes])
		}
		n := utf8.RuneCountInString(text)
		if total+n > maxNoteRunes {
			break
		}
		total += n
		lines = append(lines, "- "+text)
	}
	if len(lines) == 1 {
		return msgs
	}
	note := strings.Join(lines, "\n")

	out := make([]json.RawMessage, 0, len(msgs)+1)
	if len(msgs) > 0 {
		if role, _ := messageText(msgs[0]); role == "system" {
			if edited, ok := appendNote(msgs[0], note); ok {
				return append([]json.RawMessage{edited}, msgs[1:]...)
			}
		}
	}
	sys, _ := json.Marshal(map[string]string{"role": "system", "content": note})
	out = append(out, sys)
	return append(out, msgs...)
}

// appendNote adds the note to one system message's content, keeping its other
// fields. It is false when the content is neither a string nor a parts array.
func appendNote(raw json.RawMessage, note string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	var s string
	var parts []json.RawMessage
	var content any
	switch {
	case json.Unmarshal(m["content"], &s) == nil:
		content = s + "\n\n" + note
	case json.Unmarshal(m["content"], &parts) == nil:
		part, _ := json.Marshal(map[string]string{"type": "text", "text": note})
		content = append(append([]json.RawMessage{}, parts...), part)
	default:
		return nil, false
	}
	enc, err := json.Marshal(content)
	if err != nil {
		return nil, false
	}
	m["content"] = enc
	edited, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return edited, true
}

const (
	recallTTL        = 2 * time.Minute
	maxRecallEntries = 1024
)

type recallKey struct {
	owner string
	query [sha256.Size]byte
}

type recallEntry struct {
	mems    []memory.Memory
	expires time.Time
}

// RecallCache remembers recall results for a short time, keyed by owner and
// query, so the repeated steps of one agent tool loop do not each search the
// memory store. It holds at most maxRecallEntries entries and drops the oldest
// inserted first. It is safe for concurrent use.
type RecallCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[recallKey]recallEntry
	order   []recallKey // keys in insertion order, oldest first, no duplicates
}

// NewRecallCache returns an empty cache.
func NewRecallCache() *RecallCache {
	return &RecallCache{now: time.Now, entries: map[recallKey]recallEntry{}}
}

func newRecallKey(owner, query string) recallKey {
	return recallKey{owner: owner, query: sha256.Sum256([]byte(query))}
}

// get returns the cached result for owner and query. A cached empty result is
// a hit; an expired entry is a miss and is removed.
func (c *RecallCache) get(owner, query string) ([]memory.Memory, bool) {
	k := newRecallKey(owner, query)
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok {
		return nil, false
	}
	if !c.now().Before(e.expires) {
		delete(c.entries, k)
		c.dropFromOrder(func(o recallKey) bool { return o == k })
		return nil, false
	}
	return e.mems, true
}

// put stores a result. Putting an existing key refreshes its value and expiry
// but keeps its place in the eviction order.
func (c *RecallCache) put(owner, query string, mems []memory.Memory) {
	k := newRecallKey(owner, query)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[k]; !ok {
		for len(c.order) >= maxRecallEntries {
			delete(c.entries, c.order[0])
			c.order = c.order[1:]
		}
		c.order = append(c.order, k)
	}
	c.entries[k] = recallEntry{mems: mems, expires: c.now().Add(recallTTL)}
}

// Forget drops every cached result for owner, so a deleted memory is not
// recalled again from the cache.
func (c *RecallCache) Forget(owner string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if k.owner == owner {
			delete(c.entries, k)
		}
	}
	c.dropFromOrder(func(k recallKey) bool { return k.owner == owner })
}

// dropFromOrder removes the keys the predicate matches from the order list.
// The caller holds c.mu.
func (c *RecallCache) dropFromOrder(match func(recallKey) bool) {
	kept := c.order[:0]
	for _, k := range c.order {
		if !match(k) {
			kept = append(kept, k)
		}
	}
	c.order = kept
}

// answer collects the text of a model answer, up to maxAnswerRunes runes. A nil
// *answer collects nothing.
type answer struct {
	b     strings.Builder
	runes int
}

// add appends s, cut to the runes still allowed.
func (a *answer) add(s string) {
	if a == nil || s == "" || a.runes >= maxAnswerRunes {
		return
	}
	if n := utf8.RuneCountInString(s); a.runes+n > maxAnswerRunes {
		s = string([]rune(s)[:maxAnswerRunes-a.runes])
	}
	a.b.WriteString(s)
	a.runes += utf8.RuneCountInString(s)
}

// addDelta appends the choices[0].delta.content string of one streamed event.
// A null content, a tool-call-only delta or data that is not such an event
// adds nothing.
func (a *answer) addDelta(data []byte) {
	if a == nil || a.runes >= maxAnswerRunes {
		return
	}
	var v struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &v) == nil && len(v.Choices) > 0 {
		a.add(v.Choices[0].Delta.Content)
	}
}

// addMessage appends the choices[0].message.content string of an answer that
// was not streamed.
func (a *answer) addMessage(body []byte) {
	if a == nil {
		return
	}
	var v struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &v) == nil && len(v.Choices) > 0 {
		a.add(v.Choices[0].Message.Content)
	}
}

func (a *answer) String() string {
	if a == nil {
		return ""
	}
	return a.b.String()
}

// startLearning learns from a typed turn in the background, on a context that
// outlives the request. An overlong "remember that" sentence is skipped, and
// so is the turn when learnSlots learn calls are already running.
func (p *Proxy) startLearning(ctx context.Context, person, text, ans string) {
	capture, explicit := explicitRemember(text)
	if explicit && utf8.RuneCountInString(capture) > maxRememberRunes {
		slog.Info("memory.learn_skipped", "user", person, "reason", "too_long")
		return
	}
	p.learnSlotsOnce.Do(func() { p.learnSlots = make(chan struct{}, learnSlots) })
	select {
	case p.learnSlots <- struct{}{}:
	default:
		slog.Info("memory.learn_skipped", "user", person, "reason", "busy")
		return
	}
	ctx = context.WithoutCancel(ctx)
	p.learning.Add(1)
	go func() {
		defer p.learning.Done()
		defer func() { <-p.learnSlots }()
		// A panic here would end the whole process, because only handler
		// goroutines are recovered. The request is already answered, so log the
		// stack (not the panic value, which may carry typed or memory text) and
		// go on. This runs first, before the slot is released.
		defer func() {
			if recover() != nil {
				slog.Error("memory.learn_panicked", "user", person, "stack", string(debug.Stack()))
			}
		}()
		p.learn(ctx, person, text, capture, explicit, ans)
	}()
}

// learn stores the "remember that" capture when explicit, else the typed text
// with the answer, and logs the outcome without any of the text.
func (p *Proxy) learn(ctx context.Context, person, text, capture string, explicit bool, ans string) {
	var got []memory.Memory
	var err error
	if explicit {
		got, err = p.Memory.Remember(ctx, person, capture)
	} else {
		msgs := []memory.Message{{Role: "user", Text: text}}
		if ans != "" {
			msgs = append(msgs, memory.Message{Role: "assistant", Text: ans})
		}
		got, err = p.Memory.Add(ctx, person, "", msgs)
	}
	switch {
	case errors.Is(err, memory.ErrWriteRefused):
		slog.Info("memory.write_refused", "user", person, "reason", "ssn")
	case err != nil:
		slog.Warn("memory.learn_failed", "user", person, "err", err)
	default:
		slog.Info("memory.learned", "user", person, "count", len(got))
	}
}
