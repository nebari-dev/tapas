// Package memory keeps a person's long-term memories in the mem0 pack. Every
// call names the person (mem0's user_id), so one person's memories are never
// read or changed on behalf of another.
package memory

import (
	"context"
	"errors"
	"time"
)

// Message is one turn of an exchange to learn from; the same fields as the
// planned policy.Message.
type Message struct{ Role, Text string }

// Memory is one stored memory.
type Memory struct {
	ID, Text, UserID, AgentID string
	// Score is how well a search result matched; only Search sets it.
	Score                float64
	CreatedAt, UpdatedAt time.Time
}

var (
	// ErrNotFound is a memory that does not exist (or, in Service, is not the
	// asker's).
	ErrNotFound = errors.New("memory not found")
	// ErrRefused is mem0 refusing the API key.
	ErrRefused = errors.New("mem0 refused the key")
	// ErrInvalid is a request that would not name one person: an empty or
	// wildcard owner, or a search limit of zero or less.
	ErrInvalid = errors.New("invalid memory request")
)

// Client is the memory store.
type Client interface {
	Search(ctx context.Context, owner, agent, query string, limit int) ([]Memory, error)
	// Add returns the memories mem0 stored; empty when nothing was extracted.
	Add(ctx context.Context, owner, agent string, msgs []Message) ([]Memory, error)
	List(ctx context.Context, owner string) ([]Memory, error)
	Get(ctx context.Context, id string) (Memory, error)
	Delete(ctx context.Context, id string) error
}
