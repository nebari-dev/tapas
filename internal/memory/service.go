package memory

import (
	"context"
)

// WriteCheck decides whether msgs may be remembered for owner. Nil from it
// means allow; the policy engine fills it later.
type WriteCheck func(ctx context.Context, owner, agent string, msgs []Message) error

// Service is what the rest of Tapas uses for memory. It acts for one named
// person per call and treats another person's memory as not found, whatever
// the store behind it does.
type Service struct {
	c     Client
	check WriteCheck
}

// NewService returns a Service over c. A nil check allows every write.
func NewService(c Client, check WriteCheck) *Service { return &Service{c: c, check: check} }

// Search returns owner's memories closest to query, at most limit. A
// non-empty agent narrows it to that agent's.
func (s *Service) Search(ctx context.Context, owner, agent, query string, limit int) ([]Memory, error) {
	if err := checkScopes(owner, agent); err != nil {
		return nil, err
	}
	got, err := s.c.Search(ctx, owner, agent, query, limit)
	if err != nil {
		return nil, err
	}
	return ownedBy(owner, got), nil
}

// Add remembers what msgs say for owner, once the write check allows it.
func (s *Service) Add(ctx context.Context, owner, agent string, msgs []Message) ([]Memory, error) {
	if err := checkScopes(owner, agent); err != nil {
		return nil, err
	}
	if s.check != nil {
		if err := s.check(ctx, owner, agent, msgs); err != nil {
			return nil, err
		}
	}
	return s.c.Add(ctx, owner, agent, msgs)
}

// List returns owner's memories.
func (s *Service) List(ctx context.Context, owner string) ([]Memory, error) {
	if err := checkOwner(owner); err != nil {
		return nil, err
	}
	got, err := s.c.List(ctx, owner)
	if err != nil {
		return nil, err
	}
	return ownedBy(owner, got), nil
}

// Get returns the memory with id if it is owner's; another person's is
// ErrNotFound, the same as one that does not exist.
func (s *Service) Get(ctx context.Context, owner, id string) (Memory, error) {
	if err := checkOwner(owner); err != nil {
		return Memory{}, err
	}
	m, err := s.c.Get(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	if m.UserID != owner {
		return Memory{}, ErrNotFound
	}
	return m, nil
}

// Delete removes the memory with id if it is owner's. It looks the memory up
// first, so another person's id never reaches the store's delete.
func (s *Service) Delete(ctx context.Context, owner, id string) error {
	if _, err := s.Get(ctx, owner, id); err != nil {
		return err
	}
	return s.c.Delete(ctx, id)
}

func checkScopes(owner, agent string) error {
	if err := checkOwner(owner); err != nil {
		return err
	}
	return checkAgent(agent)
}

// ownedBy keeps the memories whose UserID is exactly owner, so a store that
// returns someone else's cannot leak it.
func ownedBy(owner string, in []Memory) []Memory {
	out := make([]Memory, 0, len(in))
	for _, m := range in {
		if m.UserID == owner {
			out = append(out, m)
		}
	}
	return out
}
