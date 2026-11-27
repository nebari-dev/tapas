package memory

import (
	"context"
	"log/slog"
	"time"
)

// startCheckPerson is a person no real account uses; the start check lists
// their memories, which reads nothing of anyone's. No real person can have this
// name: every identity name ends in "-" and hex digits (naming.IdentityName),
// and "check" is not hex.
const startCheckPerson = "tapas-start-check"

// Probe asks mem0 at url, through s, for the memories of a reserved person
// and logs whether it answered: memory.ready, or memory.unreachable with the
// error. It returns that error. The error text carries no key, and neither
// does the log.
func Probe(ctx context.Context, s *Service, url string) error {
	if _, err := s.List(ctx, startCheckPerson); err != nil {
		slog.Warn("memory.unreachable", "url", url, "err", err)
		return err
	}
	slog.Info("memory.ready", "url", url)
	return nil
}

const (
	firstWait = 5 * time.Second
	maxWait   = time.Minute
)

// WaitReady asks mem0 at url, through s, until it answers, then returns. After
// each failure it waits 5s, doubling up to one minute. It logs
// memory.unreachable (url and err) on the first failure and again whenever the
// error text changes, and memory.ready once when mem0 answers. It also returns
// when ctx ends. The error text carries no key, and neither does the log.
func WaitReady(ctx context.Context, s *Service, url string) {
	waitReady(ctx, s, url, sleepContext)
}

// sleepContext waits d and reports true, or reports false at once when ctx
// ends first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitReady(ctx context.Context, s *Service, url string, sleep func(context.Context, time.Duration) bool) {
	wait := firstWait
	lastErr := ""
	for {
		_, err := s.List(ctx, startCheckPerson)
		if err == nil {
			slog.Info("memory.ready", "url", url)
			return
		}
		if ctx.Err() != nil {
			return
		}
		if msg := err.Error(); msg != lastErr {
			slog.Warn("memory.unreachable", "url", url, "err", err)
			lastErr = msg
		}
		if !sleep(ctx, wait) {
			return
		}
		wait = min(wait*2, maxWait)
	}
}
