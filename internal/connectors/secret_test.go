package connectors_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors"
)

// A Secret prints, logs and encodes as [redacted] however it is reached;
// only Reveal gives its value.
func TestSecretNeverPrints(t *testing.T) {
	const value = "s3ntinel-token"
	s := connectors.Secret(value)
	holder := struct{ Bearer connectors.Secret }{s}

	var out []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		out = append(out, fmt.Sprintf(verb, s), fmt.Sprintf(verb, holder))
	}
	out = append(out, fmt.Sprint(s), fmt.Sprintln(s), fmt.Sprint(&s))
	var logs bytes.Buffer
	for _, h := range []slog.Handler{slog.NewTextHandler(&logs, nil), slog.NewJSONHandler(&logs, nil)} {
		slog.New(h).Info("x", "bearer", s, "holder", holder, "args", []any{s})
	}
	out = append(out, logs.String())
	j, err := json.Marshal(holder)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, string(j))

	for _, o := range out {
		if strings.Contains(o, value) || strings.Contains(o, fmt.Sprintf("%x", value)) {
			t.Errorf("leaked: %s", o)
		}
		if !strings.Contains(o, "[redacted]") {
			t.Errorf("not marked redacted: %s", o)
		}
	}
	if s.Reveal() != value {
		t.Fatalf("Reveal = %q", s.Reveal())
	}
}
