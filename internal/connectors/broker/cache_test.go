package broker

import (
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The cache holds tool lists up to a total size, not just a count: a
// connector may send lists of up to a megabyte each, for every person.
func TestTheListCacheStaysWithinItsSize(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	c := newCache(func() time.Time { return now })
	c.maxBytes = 10_000
	list := []*sdk.Tool{{Name: "t", Description: strings.Repeat("x", 3_000)}}
	for _, s := range []string{"a", "b", "c", "d"} {
		now = now.Add(time.Second)
		c.put(cacheKey{id: "c-1", rev: "r-1", subject: s}, list)
	}
	if c.bytes > c.maxBytes {
		t.Fatalf("cache holds %d bytes, more than %d", c.bytes, c.maxBytes)
	}
	if _, ok := c.get(cacheKey{id: "c-1", rev: "r-1", subject: "a"}); ok {
		t.Fatal("the oldest list was kept")
	}
	if _, ok := c.get(cacheKey{id: "c-1", rev: "r-1", subject: "d"}); !ok {
		t.Fatal("the newest list was dropped")
	}
}
