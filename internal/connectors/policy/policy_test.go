package policy_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors/policy"
)

type fixed struct {
	d   policy.Decision
	err error
}

func (f fixed) Check(context.Context, policy.Input) (policy.Decision, error) { return f.d, f.err }

// Evaluate fails closed: anything but a well-formed answer denies, with a
// reason.
func TestEvaluate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		c      policy.Checker
		effect policy.Effect
		reason string // empty: any non-empty reason
	}{
		{"allow passes", fixed{d: policy.Decision{Effect: policy.Allow}}, policy.Allow, "-"},
		{"allow everything", policy.AllowAll{}, policy.Allow, "-"},
		{"approval passes through", fixed{d: policy.Decision{Effect: policy.RequireApproval, Reason: "writes need a look"}}, policy.RequireApproval, "writes need a look"},
		{"deny keeps its reason", fixed{d: policy.Decision{Effect: policy.Deny, Reason: "not on weekends"}}, policy.Deny, "not on weekends"},
		{"deny without a reason gets one", fixed{d: policy.Decision{Effect: policy.Deny}}, policy.Deny, ""},
		{"error denies", fixed{d: policy.Decision{Effect: policy.Allow}, err: errors.New("engine down")}, policy.Deny, ""},
		{"unknown effect denies", fixed{d: policy.Decision{Effect: "maybe"}}, policy.Deny, ""},
		{"empty effect denies", fixed{}, policy.Deny, ""},
		{"nil checker denies", nil, policy.Deny, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := policy.Evaluate(context.Background(), tc.c, policy.Input{Phase: policy.PhaseRead, Connector: "slack", Tool: "post_message"})
			if d.Effect != tc.effect {
				t.Fatalf("effect %q, want %q", d.Effect, tc.effect)
			}
			switch {
			case tc.reason == "-":
			case tc.reason == "" && d.Reason == "":
				t.Fatal("denied without a reason")
			case tc.reason != "" && d.Reason != tc.reason:
				t.Fatalf("reason %q, want %q", d.Reason, tc.reason)
			}
		})
	}
}

// An error's text can carry what the checker saw; it never reaches the
// reason the agent is shown.
func TestEvaluateDoesNotLeakTheError(t *testing.T) {
	d := policy.Evaluate(context.Background(), fixed{err: errors.New("token xoxb-123")}, policy.Input{})
	if d.Reason == "" || strings.Contains(d.Reason, "xoxb") {
		t.Fatalf("reason %q", d.Reason)
	}
}
