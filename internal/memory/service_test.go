package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nebari-dev/javo/internal/memory"
	"github.com/nebari-dev/javo/internal/memory/memorytest"
)

const (
	aliceID = "0b8f7c3e-1d2a-4f5b-9c6d-7e8f9a0b1c2d"
	bobID   = "1c9a8d4f-2e3b-4a6c-8d7e-8f9a0b1c2d3e"
)

func seeded() *memorytest.Fake {
	return memorytest.New(
		memory.Memory{ID: aliceID, Text: "likes tea", UserID: "alice"},
		memory.Memory{ID: bobID, Text: "likes coffee", UserID: "bob"},
	)
}

func countCalls(f *memorytest.Fake, method string) int {
	n := 0
	for _, c := range f.Calls() {
		if c.Method == method {
			n++
		}
	}
	return n
}

func TestServiceGetAndDeleteActOnlyForTheOwner(t *testing.T) {
	tests := []struct {
		name        string
		run         func(s *memory.Service) error
		wantErr     error
		wantDeletes int
		wantLeft    int
	}{
		{"get of someone else's memory is not found", func(s *memory.Service) error {
			_, err := s.Get(context.Background(), "alice", bobID)
			return err
		}, memory.ErrNotFound, 0, 2},
		{"delete of someone else's memory is not found and never reaches the store", func(s *memory.Service) error {
			return s.Delete(context.Background(), "alice", bobID)
		}, memory.ErrNotFound, 0, 2},
		{"get of the owner's own memory returns it", func(s *memory.Service) error {
			m, err := s.Get(context.Background(), "alice", aliceID)
			if err == nil && m.Text != "likes tea" {
				return errors.New("wrong memory")
			}
			return err
		}, nil, 0, 2},
		{"delete of the owner's own memory removes it", func(s *memory.Service) error {
			return s.Delete(context.Background(), "alice", aliceID)
		}, nil, 1, 1},
		{"get of an unknown id is not found", func(s *memory.Service) error {
			_, err := s.Get(context.Background(), "alice", "2d0b9e50-3f4c-4b7d-9e8f-9a0b1c2d3e4f")
			return err
		}, memory.ErrNotFound, 0, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := seeded()
			err := tt.run(memory.NewService(f, nil))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got := countCalls(f, "Delete"); got != tt.wantDeletes {
				t.Errorf("store saw %d deletes, want %d", got, tt.wantDeletes)
			}
			left, _ := f.List(context.Background(), "alice")
			bobs, _ := f.List(context.Background(), "bob")
			if len(left)+len(bobs) != tt.wantLeft {
				t.Errorf("%d memories left, want %d", len(left)+len(bobs), tt.wantLeft)
			}
		})
	}
}

func TestServiceDropsResultsThatAreNotTheOwners(t *testing.T) {
	leaks := []memory.Memory{
		{ID: "3e1c0f61-4a5d-4c8e-8f9a-0b1c2d3e4f50", Text: "bob's", UserID: "bob"},
		{ID: "4f2d1a72-5b6e-4d9f-9a0b-1c2d3e4f5061", Text: "nobody's", UserID: ""},
		{ID: "5a3e2b83-6c7f-4e0a-8b1c-2d3e4f506172", Text: "Alice's", UserID: "Alice"},
	}
	tests := []struct {
		name string
		run  func(s *memory.Service) ([]memory.Memory, error)
	}{
		{"search", func(s *memory.Service) ([]memory.Memory, error) {
			return s.Search(context.Background(), "alice", "", "tea", 5)
		}},
		{"list", func(s *memory.Service) ([]memory.Memory, error) {
			return s.List(context.Background(), "alice")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name+" keeps only exactly the owner's memories", func(t *testing.T) {
			f := seeded()
			f.Leak = leaks
			got, err := tt.run(memory.NewService(f, nil))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].ID != aliceID {
				t.Fatalf("got %+v, want only alice's memory", got)
			}
		})
	}
}

func TestServiceRefusesWithoutAPersonBeforeCallingTheStore(t *testing.T) {
	ctx := context.Background()
	for _, owner := range []string{"", "*", " alice", "alice\n", "   "} {
		methods := map[string]func(s *memory.Service) error{
			"search": func(s *memory.Service) error { _, err := s.Search(ctx, owner, "", "q", 3); return err },
			"add": func(s *memory.Service) error {
				_, err := s.Add(ctx, owner, "", []memory.Message{{Role: "user", Text: "hi"}})
				return err
			},
			"list":   func(s *memory.Service) error { _, err := s.List(ctx, owner); return err },
			"get":    func(s *memory.Service) error { _, err := s.Get(ctx, owner, aliceID); return err },
			"delete": func(s *memory.Service) error { return s.Delete(ctx, owner, aliceID) },
		}
		for name, run := range methods {
			t.Run(name+" with owner "+owner, func(t *testing.T) {
				f := seeded()
				if err := run(memory.NewService(f, nil)); !errors.Is(err, memory.ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				if n := len(f.Calls()); n != 0 {
					t.Errorf("store saw %d calls, want none", n)
				}
			})
		}
	}
}

func TestServiceAddRunsTheWriteCheckFirst(t *testing.T) {
	errDenied := errors.New("denied")
	msgs := []memory.Message{{Role: "user", Text: "I like tea"}}
	tests := []struct {
		name      string
		check     memory.WriteCheck
		wantErr   error
		wantAdded int
	}{
		{"a refusing check adds nothing and its error is returned", func(context.Context, string, string, []memory.Message) error { return errDenied }, errDenied, 0},
		{"no check adds", nil, nil, 1},
		{"a passing check adds", func(context.Context, string, string, []memory.Message) error { return nil }, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := memorytest.New()
			got, err := memory.NewService(f, tt.check).Add(context.Background(), "alice", "agent-1", msgs)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if n := countCalls(f, "Add"); n != tt.wantAdded {
				t.Errorf("store saw %d adds, want %d", n, tt.wantAdded)
			}
			if tt.wantAdded == 1 && (len(got) != 1 || got[0].UserID != "alice" || got[0].AgentID != "agent-1") {
				t.Errorf("got %+v, want one memory for alice and agent-1", got)
			}
		})
	}
}
