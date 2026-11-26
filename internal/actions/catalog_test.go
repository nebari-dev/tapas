package actions_test

import (
	"context"
	"testing"

	"github.com/nebari-dev/javo/internal/actions"
)

type stub struct{ info actions.Info }

func (s stub) Info() actions.Info { return s.info }
func (s stub) Plan(_ context.Context, p actions.Proposal) (actions.Planned, error) {
	return actions.Planned{Proposal: p}, nil
}
func (s stub) Apply(context.Context, actions.Proposal) error { return nil }

// A hidden action is left out of what agents and models are told they may
// propose (so no propose tool is made for it), but a proposal naming it is
// still planned and approved like any other.
func TestHiddenActionsAreNotDescribedButStillPlanned(t *testing.T) {
	c := actions.NewCatalog(stub{actions.Info{ID: "restart"}}, stub{actions.Info{ID: "connector-call", Hidden: true}})
	got := c.Describe()
	if len(got) != 1 || got[0].ID != "restart" {
		t.Fatalf("described %+v", got)
	}
	if _, ok := c.Get("connector-call"); !ok {
		t.Fatal("hidden action not found")
	}
	if _, err := c.Plan(context.Background(), actions.Proposal{Action: "connector-call"}); err != nil {
		t.Fatal(err)
	}
}
