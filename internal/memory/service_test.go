package memory_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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

// logCapture collects the log records made while a test runs.
type logCapture struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r)
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// capture routes the default logger to a logCapture until the test ends.
func capture(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	old := slog.Default()
	slog.SetDefault(slog.New(c))
	t.Cleanup(func() { slog.SetDefault(old) })
	return c
}

// text renders a record as message plus attributes, for substring checks.
func text(r slog.Record) string {
	s := r.Message
	r.Attrs(func(a slog.Attr) bool { s += " " + a.Key + "=" + a.Value.String(); return true })
	return s
}

func TestServiceWarnsOnceWhenSearchOrListDropsForeignResults(t *testing.T) {
	const (
		bobsID  = "3e1c0f61-4a5d-4c8e-8f9a-0b1c2d3e4f50"
		otherID = "4f2d1a72-5b6e-4d9f-9a0b-1c2d3e4f5061"
	)
	leaks := []memory.Memory{
		{ID: bobsID, Text: "bobs private text", UserID: "bobuser"},
		{ID: otherID, Text: "second private text", UserID: "caroluser"},
	}
	tests := []struct {
		name string
		run  func(s *memory.Service) error
	}{
		{"search", func(s *memory.Service) error {
			_, err := s.Search(context.Background(), "alice", "", "tea", 5)
			return err
		}},
		{"list", func(s *memory.Service) error { _, err := s.List(context.Background(), "alice"); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := capture(t)
			f := seeded()
			f.Leak = leaks
			if err := tt.run(memory.NewService(f, nil)); err != nil {
				t.Fatal(err)
			}
			if len(logs.recs) != 1 {
				t.Fatalf("%d log records, want exactly one", len(logs.recs))
			}
			r := logs.recs[0]
			got := text(r)
			if r.Level != slog.LevelWarn || r.Message != "memory.foreign_dropped" {
				t.Fatalf("record = %s at %v", got, r.Level)
			}
			for _, want := range []string{"count=2", bobsID, otherID} {
				if !strings.Contains(got, want) {
					t.Errorf("record %q lacks %q", got, want)
				}
			}
			for _, secret := range []string{"private text", "bobuser", "caroluser"} {
				if strings.Contains(got, secret) {
					t.Errorf("record %q holds %q: only ids may be logged", got, secret)
				}
			}
		})
	}
}

func TestServiceDoesNotWarnWhenNothingIsDroppedOrOnAGetMismatch(t *testing.T) {
	logs := capture(t)
	s := memory.NewService(seeded(), nil)
	ctx := context.Background()
	if _, err := s.Search(ctx, "alice", "", "tea", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "alice", bobID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "alice", bobID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Delete err = %v, want ErrNotFound", err)
	}
	if len(logs.recs) != 0 {
		t.Fatalf("%d log records, want none: %s", len(logs.recs), text(logs.recs[0]))
	}
}

func TestServiceSearchWithAnAgentDropsResultsOfAnotherAgentAndCountsThem(t *testing.T) {
	const (
		mineID  = "6b4f3c94-7d80-4f1b-9c2d-3e4f50617283"
		otherID = "7c504da5-8e91-4a2c-8d3e-4f5061728394"
	)
	logs := capture(t)
	f := memorytest.New(memory.Memory{ID: mineID, Text: "a", UserID: "alice", AgentID: "helper"})
	f.Leak = []memory.Memory{{ID: otherID, Text: "b", UserID: "alice", AgentID: "other"}}
	got, err := memory.NewService(f, nil).Search(context.Background(), "alice", "helper", "q", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != mineID {
		t.Fatalf("got %+v, want only the helper's memory", got)
	}
	if len(logs.recs) != 1 || !strings.Contains(text(logs.recs[0]), "count=1") || !strings.Contains(text(logs.recs[0]), otherID) {
		t.Fatalf("log records = %d, want one naming the dropped id", len(logs.recs))
	}
}

func TestServiceRefusesABadAgentBeforeAnyClientCall(t *testing.T) {
	ctx := context.Background()
	for _, agent := range []string{"*", " helper", "helper\n"} {
		t.Run(fmt.Sprintf("%q", agent), func(t *testing.T) {
			f := seeded()
			s := memory.NewService(f, nil)
			if _, err := s.Search(ctx, "alice", agent, "q", 3); !errors.Is(err, memory.ErrInvalid) {
				t.Errorf("Search err = %v, want ErrInvalid", err)
			}
			if _, err := s.Add(ctx, "alice", agent, []memory.Message{{Role: "user", Text: "hi"}}); !errors.Is(err, memory.ErrInvalid) {
				t.Errorf("Add err = %v, want ErrInvalid", err)
			}
			if n := len(f.Calls()); n != 0 {
				t.Errorf("store saw %d calls, want none", n)
			}
		})
	}
}

func TestServiceCapsTheIDsInTheWarningButKeepsTheFullCount(t *testing.T) {
	logs := capture(t)
	f := seeded()
	for i := 0; i < 45; i++ {
		f.Leak = append(f.Leak, memory.Memory{ID: fmt.Sprintf("id-%02d", i), UserID: "bob"})
	}
	if _, err := memory.NewService(f, nil).List(context.Background(), "alice"); err != nil {
		t.Fatal(err)
	}
	if len(logs.recs) != 1 {
		t.Fatalf("%d log records, want one", len(logs.recs))
	}
	got := text(logs.recs[0])
	if !strings.Contains(got, "count=45") {
		t.Errorf("record %q lacks the full count", got)
	}
	if n := strings.Count(got, "id-"); n != 20 {
		t.Errorf("record names %d ids, want 20", n)
	}
	if !strings.Contains(got, "id-19") || strings.Contains(got, "id-20") {
		t.Errorf("record %q should hold the first 20 ids only", got)
	}
}

func TestRememberRunsTheWriteCheck(t *testing.T) {
	tests := []struct {
		name         string
		check        memory.WriteCheck
		wantErr      error
		wantRemember int
	}{
		{"a refusing check stores nothing and its error is returned", func(context.Context, string, string, []memory.Message) error { return memory.ErrWriteRefused }, memory.ErrWriteRefused, 0},
		{"no check stores", nil, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := memorytest.New()
			got, err := memory.NewService(f, tt.check).Remember(context.Background(), "alice", "I like tea")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if n := countCalls(f, "Remember"); n != tt.wantRemember {
				t.Errorf("store saw %d remembers, want %d", n, tt.wantRemember)
			}
			if tt.wantRemember == 1 && (len(got) != 1 || got[0].UserID != "alice" || got[0].Text != "I like tea") {
				t.Errorf("got %+v, want one memory for alice", got)
			}
		})
	}
}
