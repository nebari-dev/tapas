package connectors_test

import (
	"fmt"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors"
)

// A vendor's error code is kept only when it looks like one; anything else
// it sent (which could echo a token or a person's data) is dropped.
func TestOAuthErrorKeepsOnlyACode(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"invalid_grant":               "invalid_grant",
		"":                            "server_error",
		"invalid grant: token at-123": "server_error",
		"Invalid_Grant":               "server_error",
		"<script>":                    "server_error",
	} {
		if got := connectors.NewOAuthError(in).Code; got != want {
			t.Errorf("NewOAuthError(%q).Code = %q, want %q", in, got, want)
		}
	}
	err := fmt.Errorf("refresh: %w", connectors.NewOAuthError("invalid_grant"))
	if !connectors.IsInvalidGrant(err) || connectors.IsInvalidGrant(connectors.NewOAuthError("invalid_client")) {
		t.Fatal("IsInvalidGrant does not tell invalid_grant apart")
	}
}
