package memory_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/memory"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// The start check logs whether mem0 answered, with the address and never the
// key, and returns the failure so a caller can act on it.
func TestProbeLogsWhetherMem0Answers(t *testing.T) {
	const key = "SECRET-API-KEY"
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
		want    []string
	}{
		{"an empty list is ready", http.StatusOK, `{"results":[]}`, nil, []string{"memory.ready", "level=INFO"}},
		{"a refused key is unreachable", http.StatusUnauthorized, `{"detail":"no"}`, memory.ErrRefused, []string{"memory.unreachable", "level=WARN", "err="}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			var asked string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = r.URL.Query().Get("user_id")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			svc := memory.NewService(memory.NewMem0(srv.URL, key, srv.Client()), nil)

			err := memory.Probe(context.Background(), svc, srv.URL)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Probe error = %v, want %v", err, tt.wantErr)
			}
			if asked != "tapas-start-check" {
				t.Errorf("listed for %q, want the reserved start-check person", asked)
			}
			out := logs.String()
			for _, w := range append(tt.want, srv.URL) {
				if !strings.Contains(out, w) {
					t.Errorf("log %q lacks %q", out, w)
				}
			}
			if strings.Contains(out, key) {
				t.Errorf("log carries the key: %q", out)
			}
		})
	}
}
