package connectors

import (
	"fmt"
	"io"
	"log/slog"
)

// Secret is a value that must never be logged, printed or sent anywhere
// but where it belongs: a vendor token, a client secret, a PKCE verifier,
// an OAuth state or code. It logs, prints and encodes as [redacted]; only
// Reveal gives the value, at the one place it is used.
//
// fmt does not call a field's methods when the field is unexported, so a
// struct that holds a Secret in an unexported field and may be printed
// whole needs its own Format.
type Secret string

// redacted is what a Secret shows instead of its value.
const redacted = "[redacted]"

// Reveal is the value itself.
func (s Secret) Reveal() string { return string(s) }

// LogValue keeps the value out of logs.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Format keeps the value out of everything fmt prints, whatever the verb.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// MarshalJSON keeps the value out of JSON, such as a log handler's.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
