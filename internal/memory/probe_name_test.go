package memory

import (
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/naming"
)

// No real person may have the start check's name. Every identity name is a
// readable part, "-", and hex digits (naming.IdentityName), so a name whose
// last part is not hex can never be one.
func TestStartCheckPersonCannotBeAnIdentityName(t *testing.T) {
	tests := []struct {
		name string
		got  string
	}{
		{"the start check person", startCheckPerson},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := strings.LastIndexByte(tt.got, '-')
			if i < 0 {
				t.Fatalf("%q has no dash, so it could be a bare name", tt.got)
			}
			for _, r := range tt.got[i+1:] {
				if !strings.ContainsRune("0123456789abcdef", r) {
					return
				}
			}
			t.Fatalf("%q ends in hex digits, so naming.IdentityName could produce it", tt.got)
		})
	}
	// Even a person whose username is the start check's name gets a hash suffix.
	if got := naming.IdentityName(startCheckPerson, "issuer", "subject", 8); got == startCheckPerson {
		t.Fatalf("IdentityName produced the reserved name %q", got)
	}
}
