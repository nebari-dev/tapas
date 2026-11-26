// Package memorytest is a memory store for tests: it keeps memories in
// memory and records every call, so no test needs a running mem0.
package memorytest

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/nebari-dev/javo/internal/memory"
)

// Call is one call the fake received; only the fields that method takes are
// set.
type Call struct {
	Method   string
	Owner    string
	Agent    string
	Query    string
	ID       string
	Limit    int
	Messages []memory.Message
}

// Fake behaves like mem0 behind the pack: it stores, finds, lists and removes
// memories by owner, without ranking.
type Fake struct {
	mu       sync.Mutex
	memories []memory.Memory
	calls    []Call

	// Leak is returned from every Search and List in addition to the real
	// results, whoever it belongs to. Set it before the fake is used, to
	// test that a caller drops what is not the owner's.
	Leak []memory.Memory
}

var _ memory.Client = (*Fake)(nil)

// New returns a fake that already holds seed.
func New(seed ...memory.Memory) *Fake {
	return &Fake{memories: append([]memory.Memory(nil), seed...)}
}

// Calls returns the calls received so far, oldest first.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

func (f *Fake) record(c Call) { f.calls = append(f.calls, c) }

// find returns owner's memories, narrowed to agent when it is set.
func (f *Fake) find(owner, agent string) []memory.Memory {
	out := []memory.Memory{}
	for _, m := range f.memories {
		if m.UserID == owner && (agent == "" || m.AgentID == agent) {
			out = append(out, m)
		}
	}
	return append(out, f.Leak...)
}

func (f *Fake) Search(_ context.Context, owner, agent, query string, limit int) ([]memory.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(Call{Method: "Search", Owner: owner, Agent: agent, Query: query, Limit: limit})
	got := f.find(owner, agent)
	if limit > 0 && len(got) > limit {
		got = got[:limit]
	}
	return got, nil
}

// Add stores one memory per user message, with a lowercase UUID as its id.
func (f *Fake) Add(_ context.Context, owner, agent string, msgs []memory.Message) ([]memory.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(Call{Method: "Add", Owner: owner, Agent: agent, Messages: append([]memory.Message(nil), msgs...)})
	now := time.Now().UTC()
	added := []memory.Memory{}
	for _, msg := range msgs {
		if msg.Role != "user" {
			continue
		}
		m := memory.Memory{ID: newID(), Text: msg.Text, UserID: owner, AgentID: agent, CreatedAt: now, UpdatedAt: now}
		f.memories = append(f.memories, m)
		added = append(added, m)
	}
	return added, nil
}

func (f *Fake) List(_ context.Context, owner string) ([]memory.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(Call{Method: "List", Owner: owner})
	return f.find(owner, ""), nil
}

// Get returns the memory with id whoever owns it, like mem0.
func (f *Fake) Get(_ context.Context, id string) (memory.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(Call{Method: "Get", ID: id})
	for _, m := range f.memories {
		if m.ID == id {
			return m, nil
		}
	}
	return memory.Memory{}, memory.ErrNotFound
}

// Delete removes the memory with id whoever owns it, like mem0.
func (f *Fake) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(Call{Method: "Delete", ID: id})
	for i, m := range f.memories {
		if m.ID == id {
			f.memories = append(f.memories[:i], f.memories[i+1:]...)
			return nil
		}
	}
	return memory.ErrNotFound
}

// newID returns a random version 4 UUID in lowercase, the shape mem0's ids
// have.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("memorytest: no randomness: " + err.Error())
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
