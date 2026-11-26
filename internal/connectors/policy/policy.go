// Package policy is the hook an organisation's own rules plug into before a
// connector tool runs. The broker asks it at each phase of a call and acts
// only on a well-formed answer: anything else denies.
package policy

import (
	"context"
	"encoding/json"
)

// Phase is the point of a call the check is for.
type Phase string

const (
	PhaseRead    Phase = "read"    // a read tool, about to be forwarded
	PhasePropose Phase = "propose" // a write tool, about to become a plan
	PhaseApply   Phase = "apply"   // an approved plan, about to be forwarded
)

// Effect is what a checker decided.
type Effect string

const (
	Allow           Effect = "allow"
	Deny            Effect = "deny"
	RequireApproval Effect = "require_approval"
)

// Input is one tool call as the checker sees it. Identity comes from the
// verified Tapas token, never from the call's arguments.
type Input struct {
	Phase     Phase
	Person    string
	Groups    []string
	Agent     string
	TokenID   string
	Connector string
	Tool      string
	Write     bool
	Arguments json.RawMessage
}

// Decision is a checker's answer. Reason is shown to the agent on a deny.
type Decision struct {
	Effect Effect
	Reason string
}

// Checker decides whether a tool call may go ahead.
type Checker interface {
	Check(ctx context.Context, in Input) (Decision, error)
}

// AllowAll allows every call: the default when no policy is configured.
type AllowAll struct{}

func (AllowAll) Check(context.Context, Input) (Decision, error) {
	return Decision{Effect: Allow}, nil
}

// Evaluate asks c about in and fails closed: a nil checker, an error or an
// unknown effect denies. A deny always carries a reason. The error's text is
// not passed on, as it may hold what the checker saw.
func Evaluate(ctx context.Context, c Checker, in Input) Decision {
	if c == nil {
		return Decision{Effect: Deny, Reason: "No policy is configured."}
	}
	d, err := c.Check(ctx, in)
	switch {
	case err != nil:
		return Decision{Effect: Deny, Reason: "The policy check failed."}
	case d.Effect == Deny && d.Reason == "":
		d.Reason = "Denied by policy."
	case d.Effect != Allow && d.Effect != Deny && d.Effect != RequireApproval:
		return Decision{Effect: Deny, Reason: "The policy gave no valid answer."}
	}
	return d
}
