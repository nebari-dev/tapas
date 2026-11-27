package gateway_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/memory"
	"github.com/nebari-dev/javo/internal/memory/memorytest"
)

const bobMemoryOwner = "bob-0000"

type memoryView struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type recordingForgetter struct {
	mu     sync.Mutex
	owners []string
}

func (r *recordingForgetter) Forget(owner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners = append(r.owners, owner)
}

func (r *recordingForgetter) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.owners...)
}

// failingMemories is a Store whose mem0 cannot be reached.
type failingMemories struct{ err error }

func (f failingMemories) List(context.Context, string) ([]memory.Memory, error) { return nil, f.err }
func (f failingMemories) Delete(context.Context, string, string) error          { return f.err }

func memoryFixture(t *testing.T) (*apitest.Fixture, *memorytest.Fake, *recordingForgetter) {
	t.Helper()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	fake := memorytest.New(
		memory.Memory{ID: "a-old", Text: "likes tea", UserID: apitest.LocalAlice(), CreatedAt: base, UpdatedAt: base},
		memory.Memory{ID: "a-new", Text: "works in Go", UserID: apitest.LocalAlice(), CreatedAt: base.Add(time.Hour), UpdatedAt: base.Add(time.Hour)},
		memory.Memory{ID: "b-1", Text: "likes coffee", UserID: bobMemoryOwner, CreatedAt: base, UpdatedAt: base},
	)
	cache := &recordingForgetter{}
	f := apitest.New(t, apitest.Options{Memories: memory.NewService(fake, nil), MemoriesCache: cache})
	return f, fake, cache
}

func memoryDeletes(fake *memorytest.Fake) []string {
	var ids []string
	for _, c := range fake.Calls() {
		if c.Method == "Delete" {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

func TestMemoriesListsOnlyYourOwnNewestFirst(t *testing.T) {
	f, _, _ := memoryFixture(t)
	for _, tc := range []struct {
		name    string
		session string
		want    []string
	}{
		{"alice sees hers, newest first", apitest.LocalAlice(), []string{"a-new", "a-old"}},
		{"bob sees his only", bobMemoryOwner, []string{"b-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.Do("GET", "/api/memories", "", f.Session(tc.session))
			wantCode(t, "list", w, http.StatusOK)
			var got []string
			for _, m := range apitest.Decode[[]memoryView](t, w) {
				got = append(got, m.ID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("ids %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMemoriesDeleteRemovesYoursAndForgetsTheRecallCache(t *testing.T) {
	f, fake, cache := memoryFixture(t)
	wantCode(t, "delete", f.Do("DELETE", "/api/memories/a-old", "", f.Session(apitest.LocalAlice())), http.StatusNoContent)
	if got := memoryDeletes(fake); len(got) != 1 || got[0] != "a-old" {
		t.Fatalf("deletes %v", got)
	}
	if got := cache.got(); len(got) != 1 || got[0] != apitest.LocalAlice() {
		t.Fatalf("recall cache forgot %v, want alice", got)
	}
	w := f.Do("GET", "/api/memories", "", f.Session(apitest.LocalAlice()))
	if got := apitest.Decode[[]memoryView](t, w); len(got) != 1 || got[0].ID != "a-new" {
		t.Fatalf("after delete: %+v", got)
	}
}

func TestMemoriesRefusesAnotherPersonsID(t *testing.T) {
	f, fake, cache := memoryFixture(t)
	wantCode(t, "delete bob's", f.Do("DELETE", "/api/memories/b-1", "", f.Session(apitest.LocalAlice())), http.StatusNotFound)
	if got := memoryDeletes(fake); len(got) != 0 {
		t.Fatalf("the store was asked to delete %v", got)
	}
	if got := cache.got(); len(got) != 0 {
		t.Fatalf("recall cache forgot %v after a refusal", got)
	}
}

func TestMemoriesWhenOffSaysSo(t *testing.T) {
	f := apitest.New(t, apitest.Options{})
	alice := f.Session(apitest.LocalAlice())
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/memories"},
		{"DELETE", "/api/memories/anything"},
	} {
		w := f.Do(tc.method, tc.path, "", alice)
		wantCode(t, tc.method+" "+tc.path, w, http.StatusNotFound)
		if !strings.Contains(w.Body.String(), "Memory is off on this platform.") {
			t.Errorf("%s %s body %s", tc.method, tc.path, w.Body)
		}
	}
}

func TestMemoriesWhenMemFailsIs502(t *testing.T) {
	const secretText = "dial tcp 10.1.2.3:8000: connection refused"
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	f := apitest.New(t, apitest.Options{Memories: failingMemories{errors.New(secretText)}})
	alice := f.Session(apitest.LocalAlice())
	for _, tc := range []struct{ method, path, event string }{
		{"GET", "/api/memories", "memory.list_failed"},
		{"DELETE", "/api/memories/some-id", "memory.delete_failed"},
	} {
		logs.Reset()
		w := f.Do(tc.method, tc.path, "", alice)
		if got := logs.String(); !strings.Contains(got, "msg="+tc.event) || !strings.Contains(got, "user="+apitest.LocalAlice()) {
			t.Errorf("%s %s log %q does not name %s and the user", tc.method, tc.path, got, tc.event)
		}
		wantCode(t, tc.method+" "+tc.path, w, http.StatusBadGateway)
		body := w.Body.String()
		if !strings.Contains(body, "Memory could not be reached; try again later.") {
			t.Errorf("%s %s body %s", tc.method, tc.path, body)
		}
		if strings.Contains(body, "10.1.2.3") || strings.Contains(body, "refused") {
			t.Errorf("%s %s leaks the error: %s", tc.method, tc.path, body)
		}
	}
}

func TestMemoriesRefusesAppTokens(t *testing.T) {
	fake := memorytest.New(memory.Memory{ID: "a-1", Text: "likes tea", UserID: apitest.LocalAlice()})
	f := newAgentFixtureWith(t, false, apitest.Options{Memories: memory.NewService(fake, nil), MemoriesCache: &recordingForgetter{}})
	wantCode(t, "list with an app token", f.agent("GET", "/api/memories", ""), http.StatusForbidden)
	wantCode(t, "delete with an app token", f.agent("DELETE", "/api/memories/a-1", ""), http.StatusForbidden)
	if got := memoryDeletes(fake); len(got) != 0 {
		t.Fatalf("an app token deleted %v", got)
	}
}
