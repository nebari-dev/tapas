package broker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/client"
)

// proposeNote ends a write tool's description, as on Tapas's own propose
// tools.
const proposeNote = " Proposes the change only: it waits for you to approve it in the browser at the approveUrl returned."

// Tools are the connector tools caller may use: for each enabled connector
// authz allows and the caller has a connection to, its tools that are not
// off, named slug__tool. A connector that fails to list is left out and
// logged.
func (b *Broker) Tools(ctx context.Context, caller connectors.Caller) []*sdk.Tool {
	if !caller.MayUseConnectors() {
		return nil
	}
	conns, err := b.c.Connectors.ListEnabled(ctx)
	if err != nil {
		slog.Error("list connectors", "err", err)
		return nil
	}
	var out []*sdk.Tool
	for _, conn := range conns {
		if !allowed(caller, conn) {
			continue
		}
		bearer, err := b.bearer(ctx, caller, conn)
		if err != nil {
			continue
		}
		tools, err := b.upstream(ctx, caller, conn, bearer)
		if err != nil {
			slog.Warn("a connector's tools could not be listed", "connector", conn.Slug, "err", err)
			continue
		}
		for _, t := range tools {
			if tl := offered(conn, t); tl != nil {
				out = append(out, tl)
			}
		}
	}
	return out
}

// offered is t as Tapas offers it, or nil when it is off or its joined name
// is too long.
func offered(conn connectors.Connector, t *sdk.Tool) *sdk.Tool {
	mode := connectors.ModeOf(conn.ToolModes, t.Name)
	if mode == connectors.ToolOff {
		return nil
	}
	name, ok := connectors.JoinTool(conn.Slug, t.Name)
	if !ok {
		slog.Warn("a connector's tool name is too long to offer", "connector", conn.Slug)
		return nil
	}
	desc := conn.Name + ": " + t.Description
	if mode == connectors.ToolWrite {
		desc += proposeNote
	}
	schema := t.InputSchema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	return &sdk.Tool{
		Name: name, Title: t.Title, Description: desc, InputSchema: schema,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: mode == connectors.ToolRead},
	}
}

// upstream is conn's own tool list, from the cache when fresh.
func (b *Broker) upstream(ctx context.Context, caller connectors.Caller, conn connectors.Connector, bearer string) ([]*sdk.Tool, error) {
	key := cacheKey{id: conn.ID, rev: conn.Rev, subject: subject(caller, conn)}
	if tools, ok := b.cache.get(key); ok {
		return tools, nil
	}
	tools, err := b.c.Forwarder.ListTools(ctx, client.Target{URL: conn.URL}, bearer)
	if err != nil {
		return nil, err
	}
	b.cache.put(key, tools)
	return tools, nil
}

// Bounds of the upstream list cache (D11).
const (
	cacheTTL     = 5 * time.Minute
	cacheEntries = 1024
)

type cacheKey struct{ id, rev, subject string }

type cacheEntry struct {
	tools []*sdk.Tool
	at    time.Time
}

// cache keeps upstream tool lists per connector revision and subject for
// cacheTTL, at most cacheEntries of them (the oldest dropped first).
type cache struct {
	now     func() time.Time
	mu      sync.Mutex
	entries map[cacheKey]cacheEntry
}

func newCache(now func() time.Time) *cache {
	return &cache{now: now, entries: map[cacheKey]cacheEntry{}}
}

func (c *cache) get(k cacheKey) ([]*sdk.Tool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	if !ok || c.now().Sub(e.at) >= cacheTTL {
		return nil, false
	}
	return e.tools, true
}

func (c *cache) put(k cacheKey, tools []*sdk.Tool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for key, e := range c.entries {
		if now.Sub(e.at) >= cacheTTL {
			delete(c.entries, key)
		}
	}
	for len(c.entries) >= cacheEntries {
		var oldest cacheKey
		first := true
		for key, e := range c.entries {
			if first || e.at.Before(c.entries[oldest].at) {
				oldest, first = key, false
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[k] = cacheEntry{tools: tools, at: now}
}
