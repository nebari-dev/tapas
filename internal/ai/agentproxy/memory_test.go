package agentproxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/memory"
)

func msgs(t *testing.T, raw ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(raw))
	for i, r := range raw {
		if !json.Valid([]byte(r)) {
			t.Fatalf("bad test json: %s", r)
		}
		out[i] = json.RawMessage(r)
	}
	return out
}

func TestLastUserTextFindsWhatThePersonTyped(t *testing.T) {
	tests := []struct {
		name      string
		in        []string
		wantText  string
		wantTyped bool
	}{
		{"last message is the user's string", []string{
			`{"role":"system","content":"s"}`, `{"role":"user","content":"  hello  "}`}, "hello", true},
		{"a tool message after the user is not typed", []string{
			`{"role":"user","content":"run it"}`, `{"role":"assistant","content":"ok"}`, `{"role":"tool","content":"out"}`}, "run it", false},
		{"array content keeps text parts only", []string{
			`{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"here"}]}`}, "look\nhere", true},
		{"no user message", []string{`{"role":"system","content":"s"}`}, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, typed := lastUserText(msgs(t, tc.in...))
			if text != tc.wantText || typed != tc.wantTyped {
				t.Fatalf("got (%q, %v), want (%q, %v)", text, typed, tc.wantText, tc.wantTyped)
			}
		})
	}
}

func TestHousekeepingSpotsPiSummariesAndLongPastes(t *testing.T) {
	// The summarization prompt is pi-coding-agent 1.4.0
	// dist/core/compaction/utils.js:145; re-check on a pi upgrade.
	summary := msgs(t,
		`{"role":"system","content":"You are a context summarization assistant. Read the conversation."}`,
		`{"role":"user","content":"<conversation>hi</conversation>"}`)
	ordinary := msgs(t,
		`{"role":"system","content":"You are an expert coding assistant operating inside pi."}`,
		`{"role":"user","content":"hi"}`)
	tests := []struct {
		name     string
		in       []json.RawMessage
		userText string
		want     bool
	}{
		{"pi summarization prompt", summary, "<conversation>hi</conversation>", true},
		{"ordinary pi system prompt", ordinary, "hi", false},
		{"8001 runes", ordinary, strings.Repeat("é", 8001), true},
		{"8000 runes", ordinary, strings.Repeat("é", 8000), false},
		{"no messages", nil, "hi", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := housekeeping(tc.in, tc.userText); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExplicitRememberIsCaseInsensitiveAndNeedsText(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"remember that I prefer tabs", "I prefer tabs", true},
		{"REMEMBER: use uv", "use uv", true},
		{"  Remember that\n  multi\nline", "multi\nline", true},
		{"remember that   ", "", false},
		{"remember to run tests", "", false},
		{"please remember that x", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := explicitRemember(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

const wantHeader = "What this person has told you in earlier conversations (from Tapas memory). Use it when it is relevant; do not mention this note."

func mems(texts ...string) []memory.Memory {
	out := make([]memory.Memory, len(texts))
	for i, s := range texts {
		out[i] = memory.Memory{Text: s}
	}
	return out
}

func decode(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWithNotePutsTheNoteInTheFirstSystemMessage(t *testing.T) {
	t.Run("string system content gets the note after a blank line", func(t *testing.T) {
		in := msgs(t, `{"role":"system","content":"be brief","name":"sys"}`, `{"role":"user","content":"hi"}`)
		orig := string(in[0])
		out := withNote(in, mems("likes tabs"))
		if len(out) != 2 || string(in[0]) != orig {
			t.Fatalf("len %d, input changed to %s", len(out), in[0])
		}
		m := decode(t, out[0])
		var content, name string
		_ = json.Unmarshal(m["content"], &content)
		_ = json.Unmarshal(m["name"], &name)
		want := "be brief\n\n" + wantHeader + "\n- likes tabs"
		if content != want || name != "sys" {
			t.Fatalf("content %q name %q", content, name)
		}
	})

	t.Run("array system content gets one more text part and keeps the image", func(t *testing.T) {
		in := msgs(t, `{"role":"system","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}}]}`)
		orig := string(in[0])
		out := withNote(in, mems("m1"))
		if string(in[0]) != orig {
			t.Fatalf("input changed: %s", in[0])
		}
		var parts []map[string]any
		if err := json.Unmarshal(decode(t, out[0])["content"], &parts); err != nil {
			t.Fatal(err)
		}
		if len(parts) != 3 || parts[1]["type"] != "image_url" || parts[2]["type"] != "text" ||
			parts[2]["text"] != wantHeader+"\n- m1" {
			t.Fatalf("parts %v", parts)
		}
	})

	t.Run("no system message inserts one at index 0", func(t *testing.T) {
		in := msgs(t, `{"role":"user","content":"hi"}`)
		out := withNote(in, mems("m1"))
		if len(out) != 2 || len(in) != 1 {
			t.Fatalf("out %d in %d", len(out), len(in))
		}
		m := decode(t, out[0])
		var role, content string
		_ = json.Unmarshal(m["role"], &role)
		_ = json.Unmarshal(m["content"], &content)
		if role != "system" || content != wantHeader+"\n- m1" {
			t.Fatalf("role %q content %q", role, content)
		}
		if string(out[1]) != string(in[0]) {
			t.Fatalf("user message changed: %s", out[1])
		}
	})

	t.Run("a system message that is not first is left alone and a new one goes at index 0", func(t *testing.T) {
		in := msgs(t, `{"role":"user","content":"hi"}`, `{"role":"system","content":"late"}`)
		out := withNote(in, mems("m1"))
		if len(out) != 3 || string(out[1]) != string(in[0]) || string(out[2]) != string(in[1]) {
			t.Fatalf("out %v", out)
		}
		m := decode(t, out[0])
		var role, content string
		_ = json.Unmarshal(m["role"], &role)
		_ = json.Unmarshal(m["content"], &content)
		if role != "system" || content != wantHeader+"\n- m1" {
			t.Fatalf("role %q content %q", role, content)
		}
	})

	noteLines := func(t *testing.T, ms []memory.Memory) []string {
		t.Helper()
		out := withNote(msgs(t, `{"role":"user","content":"hi"}`), ms)
		var content string
		_ = json.Unmarshal(decode(t, out[0])["content"], &content)
		lines := strings.Split(content, "\n")
		if lines[0] != wantHeader {
			t.Fatalf("header %q", lines[0])
		}
		return lines[1:]
	}

	t.Run("at most five memories", func(t *testing.T) {
		got := noteLines(t, mems("1", "2", "3", "4", "5", "6", "7"))
		if len(got) != 5 || got[4] != "- 5" {
			t.Fatalf("lines %v", got)
		}
	})

	t.Run("a long memory is cut to 500 runes", func(t *testing.T) {
		got := noteLines(t, mems(strings.Repeat("é", 900)))
		if len(got) != 1 || got[0] != "- "+strings.Repeat("é", 500) {
			t.Fatalf("got %d runes", len([]rune(got[0])))
		}
	})

	t.Run("five full-length memories fit the 2500 rune total", func(t *testing.T) {
		got := noteLines(t, mems(strings.Repeat("a", 500), strings.Repeat("b", 500), strings.Repeat("c", 500),
			strings.Repeat("d", 500), strings.Repeat("e", 500)))
		if len(got) != 5 {
			t.Fatalf("lines %d", len(got))
		}
	})
}

func TestRecallCacheExpiresEvictsAndForgets(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewRecallCache()
	c.now = func() time.Time { return now }
	mem := []memory.Memory{{ID: "m1", Text: "likes go"}}

	c.put("alice", "q", mem)
	now = now.Add(time.Minute + 59*time.Second)
	if got, ok := c.get("alice", "q"); !ok || len(got) != 1 {
		t.Fatalf("at 1m59s want hit with 1 memory, got %v %v", got, ok)
	}
	now = now.Add(2 * time.Second)
	if _, ok := c.get("alice", "q"); ok {
		t.Fatal("at 2m01s want miss")
	}

	c.put("alice", "empty", nil)
	if got, ok := c.get("alice", "empty"); !ok || len(got) != 0 {
		t.Fatalf("cached empty result must be a hit, got %v %v", got, ok)
	}

	c = NewRecallCache()
	c.now = func() time.Time { return now }
	for i := 0; i <= maxRecallEntries; i++ {
		c.put("alice", fmt.Sprintf("q%d", i), mem)
	}
	if _, ok := c.get("alice", "q0"); ok {
		t.Fatal("oldest entry should be evicted after 1025 puts")
	}
	if _, ok := c.get("alice", "q1"); !ok {
		t.Fatal("second entry should survive")
	}

	c = NewRecallCache()
	c.now = func() time.Time { return now }
	c.put("alice", "a", mem)
	c.put("bob", "b", mem)
	c.Forget("alice")
	if _, ok := c.get("alice", "a"); ok {
		t.Fatal("alice entry should be forgotten")
	}
	if _, ok := c.get("bob", "b"); !ok {
		t.Fatal("bob entry should be kept")
	}
}

func TestRecallCacheOverwriteKeepsOrderAndSizeBounded(t *testing.T) {
	now := time.Unix(1000, 0)
	c := NewRecallCache()
	c.now = func() time.Time { return now }
	mem := []memory.Memory{{ID: "m1"}}

	c.put("alice", "first", mem)
	for i := 0; i < 3*maxRecallEntries; i++ {
		c.put("alice", "first", mem)
	}
	if len(c.order) != 1 || len(c.entries) != 1 {
		t.Fatalf("overwrites must not grow the order list: order=%d entries=%d", len(c.order), len(c.entries))
	}
	// Refill to capacity: "first" is the oldest key, so one more put evicts it,
	// and nothing else.
	for i := 1; i < maxRecallEntries; i++ {
		c.put("alice", fmt.Sprintf("q%d", i), mem)
	}
	c.put("alice", "extra", mem)
	if _, ok := c.get("alice", "first"); ok {
		t.Fatal("oldest key should be evicted")
	}
	if _, ok := c.get("alice", "q1"); !ok {
		t.Fatal("q1 should survive")
	}
	if len(c.entries) != maxRecallEntries {
		t.Fatalf("want %d entries, got %d", maxRecallEntries, len(c.entries))
	}
}
