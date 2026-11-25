package memory

import (
	"context"
	"log/slog"
)

// startCheckPerson is a person no real account uses; the start check lists
// their memories, which reads nothing of anyone's.
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
