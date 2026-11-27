//go:build !local

package gateway_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/model"
)

// A string placed in a call's arguments (and so in the connector's
// answer, which echoes them) and in the connection's bearer appears in no
// audit row and no log line, on a call that works and on one the connector
// refuses. Not parallel: it captures the process's logs.
func TestConnectorCallsAuditAndLogNoSecrets(t *testing.T) {
	const sentinel = "s3ntinel-c-1"
	logs := apitest.CaptureLogs(t)
	fake := connectorstest.NewConnector(t, "tok-"+sentinel)
	f := newConnectorsFixture(t, "tok-"+sentinel)
	if _, err := f.Connectors.Create(context.Background(), model.Actor{Person: "alice"}, registry.Input{
		Slug: "fake", Name: "Fake", URL: fake.URL, Enabled: true,
		AllowedGroups: []string{"*"}, ToolModes: map[string]connectors.ToolMode{"echo": connectors.ToolRead},
	}); err != nil {
		t.Fatal(err)
	}
	cs := f.connect(t, f.NewToken(f.alice, `{"name":"agent","scope":"read"}`).Token)
	if res, text := callTool(t, cs, "fake__echo", map[string]any{"text": sentinel}); res.IsError || !strings.Contains(text, sentinel) {
		t.Fatalf("echo: %v %q", res.IsError, text)
	}
	fake.Unauthorized(true)
	if res, _ := callTool(t, cs, "fake__echo", map[string]any{"text": sentinel}); !res.IsError {
		t.Fatal("a refused bearer answered")
	}

	rows, err := f.Db.Audit().List(context.Background(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, r := range rows {
		if r.Action == "connector.call" {
			calls++
		}
	}
	if calls != 2 {
		t.Fatalf("%d connector.call rows", calls)
	}
	all, _ := json.Marshal(rows)
	if strings.Contains(string(all), sentinel) {
		t.Errorf("audit rows hold the sentinel: %s", all)
	}
	if strings.Contains(logs.String(), sentinel) {
		t.Errorf("logs hold the sentinel:\n%s", logs)
	}
}
