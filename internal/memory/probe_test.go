package memory_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

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

// A 404 at the start check means the address does not reach the mem0 pack
// (the pack answers 404 for routes it does not serve). It must be a failure
// that names the request and status, not an empty list and not "not found".
func TestProbeTreatsA404AsAWrongAddress(t *testing.T) {
	logs := captureLogs(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	svc := memory.NewService(memory.NewMem0(srv.URL, "k", srv.Client()), nil)

	err := memory.Probe(context.Background(), svc, srv.URL)

	if err == nil || errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("Probe error = %v, want an error that is not ErrNotFound", err)
	}
	out := logs.String()
	for _, w := range []string{"memory.unreachable", "level=WARN", "GET /memories", "404"} {
		if !strings.Contains(out, w) {
			t.Errorf("log %q lacks %q", out, w)
		}
	}
	if strings.Contains(out, "memory.ready") {
		t.Errorf("log %q says ready", out)
	}
}

// answers returns a fake mem0 that replies with each status in turn, then
// 200 with an empty list for every later request.
func answers(t *testing.T, statuses ...int) *httptest.Server {
	t.Helper()
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		if n < len(statuses) {
			status = statuses[n]
		}
		n++
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"results":[]}`))
		} else {
			_, _ = w.Write([]byte(`{"detail":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// recordSleeps returns a sleep that records each wait and never blocks.
func recordSleeps(got *[]time.Duration) func(context.Context, time.Duration) bool {
	return func(ctx context.Context, d time.Duration) bool {
		*got = append(*got, d)
		return ctx.Err() == nil
	}
}

// The start check keeps asking mem0 with a growing wait, logs the first
// failure once while the error stays the same, and logs ready when it answers.
func TestWaitReadyRetriesUntilMemAnswers(t *testing.T) {
	logs := captureLogs(t)
	srv := answers(t, 503, 503, 503)
	svc := memory.NewService(memory.NewMem0(srv.URL, "k", srv.Client()), nil)
	var slept []time.Duration

	memory.WaitReadyWith(context.Background(), svc, srv.URL, recordSleeps(&slept))

	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	if !slices.Equal(slept, want) {
		t.Errorf("waits = %v, want %v", slept, want)
	}
	out := logs.String()
	if n := strings.Count(out, "memory.unreachable"); n != 1 {
		t.Errorf("%d memory.unreachable lines, want 1: %q", n, out)
	}
	if n := strings.Count(out, "memory.ready"); n != 1 {
		t.Errorf("%d memory.ready lines, want 1: %q", n, out)
	}
	if !strings.Contains(out, srv.URL) {
		t.Errorf("log lacks the address: %q", out)
	}
}

// A different failure is news, so it is logged again.
func TestWaitReadyLogsAgainWhenTheErrorChanges(t *testing.T) {
	logs := captureLogs(t)
	srv := answers(t, 503, 503, 401)
	svc := memory.NewService(memory.NewMem0(srv.URL, "k", srv.Client()), nil)
	var slept []time.Duration

	memory.WaitReadyWith(context.Background(), svc, srv.URL, recordSleeps(&slept))

	if n := strings.Count(logs.String(), "memory.unreachable"); n != 2 {
		t.Errorf("%d memory.unreachable lines, want 2: %q", n, logs.String())
	}
	if n := strings.Count(logs.String(), "memory.ready"); n != 1 {
		t.Errorf("%d memory.ready lines, want 1", n)
	}
}

// The wait doubles from 5s but never goes past a minute.
func TestWaitReadyCapsTheWaitAtOneMinute(t *testing.T) {
	captureLogs(t)
	srv := answers(t, 503, 503, 503, 503, 503, 503, 503, 503)
	svc := memory.NewService(memory.NewMem0(srv.URL, "k", srv.Client()), nil)
	var slept []time.Duration

	memory.WaitReadyWith(context.Background(), svc, srv.URL, recordSleeps(&slept))

	s, m := time.Second, time.Minute
	want := []time.Duration{5 * s, 10 * s, 20 * s, 40 * s, m, m, m, m}
	if !slices.Equal(slept, want) {
		t.Errorf("waits = %v, want %v", slept, want)
	}
}

// When the gateway stops, the check stops and does not claim mem0 is ready.
func TestWaitReadyStopsWhenTheContextEnds(t *testing.T) {
	logs := captureLogs(t)
	srv := answers(t, 503, 503, 503)
	svc := memory.NewService(memory.NewMem0(srv.URL, "k", srv.Client()), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { memory.WaitReady(ctx, svc, srv.URL); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitReady did not return after the context ended")
	}
	if strings.Contains(logs.String(), "memory.ready") {
		t.Errorf("logged ready after the context ended: %q", logs.String())
	}
}
