//go:build mem0

package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// randomName returns prefix plus eight random hex characters, so reruns
// against the same mem0 never share a person.
func randomName(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return prefix + "-" + hex.EncodeToString(b)
}

func findID(ms []Memory, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

// These tests need a running mem0 pack: scripts/test/mem0-integration.sh
// starts one and sets MEM0_URL and MEM0_API_KEY.
func TestIntegrationOneOwnersMemoriesRoundTripAndStayTheirs(t *testing.T) {
	url, key := os.Getenv("MEM0_URL"), os.Getenv("MEM0_API_KEY")
	if url == "" || key == "" {
		t.Skip("MEM0_URL and MEM0_API_KEY are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	slow := &http.Client{Timeout: time.Minute}
	svc := NewService(NewMem0(url, key, nil, WithAddClient(slow)), nil)
	owner, other, agent := randomName(t, "alice"), randomName(t, "bob"), "helper"
	word := randomName(t, "zanzibar")

	added, err := svc.Add(ctx, owner, agent, []Message{
		{Role: "user", Text: "I keep a pet " + word + " at home"},
		{Role: "assistant", Text: "Noted."},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(added) == 0 {
		t.Fatal("Add stored nothing")
	}
	id := added[0].ID

	found, err := svc.Search(ctx, owner, agent, word, 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !findID(found, id) {
		t.Errorf("Search for %q did not find memory %s: %+v", word, id, found)
	}
	listed, err := svc.List(ctx, owner)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !findID(listed, id) {
		t.Errorf("List did not include memory %s: %+v", id, listed)
	}

	got, err := svc.Get(ctx, owner, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != id || got.UserID != owner {
		t.Errorf("Get = id %q, user %q; want id %q, user %q", got.ID, got.UserID, id, owner)
	}

	if _, err := svc.Get(ctx, other, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("another person's Get = %v, want ErrNotFound", err)
	}
	if otherFound, err := svc.Search(ctx, other, "", word, 5); err != nil || findID(otherFound, id) {
		t.Errorf("another person's Search = %+v, %v; want no sight of %s", otherFound, err, id)
	}
	if otherListed, err := svc.List(ctx, other); err != nil || findID(otherListed, id) {
		t.Errorf("another person's List = %+v, %v; want no sight of %s", otherListed, err, id)
	}
	if err := svc.Delete(ctx, other, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("another person's Delete = %v, want ErrNotFound", err)
	}
	if _, err := svc.Get(ctx, owner, id); err != nil {
		t.Errorf("owner's memory did not survive another person's Delete: %v", err)
	}

	if err := svc.Delete(ctx, owner, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, owner, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
}
