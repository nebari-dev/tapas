package authz_test

import (
	"testing"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/reasons"
)

// A connector is for the groups it names, "*" for every member. A platform
// admin configures connectors but gets no use of one from that alone.
func TestUseConnector(t *testing.T) {
	ask := func(enabled bool, groups ...string) *authz.ConnectorAsk {
		return &authz.ConnectorAsk{Enabled: enabled, Groups: groups}
	}
	adminInDS := authz.Principal{ID: "root-1b1b1b1b", Groups: []string{"/org/admins", "/org/ds"}}
	for _, tc := range []struct {
		name string
		p    authz.Principal
		c    *authz.ConnectorAsk
		want bool
		via  string
	}{
		{"member of a named group", bob, ask(true, "/org/ds"), true, "group:/org/ds"},
		{"member outside the groups", alice, ask(true, "/org/ds"), false, ""},
		{"star lets every member in", alice, ask(true, "*"), true, "member"},
		{"star needs membership", carol, ask(true, "*"), false, ""},
		{"a group named star is not every member", authz.Principal{ID: "erin-1", Groups: []string{"*"}}, ask(true, "*"), false, ""},
		{"empty list allows nobody", bob, ask(true), false, ""},
		{"disabled connector", bob, ask(false, "/org/ds"), false, ""},
		{"disabled connector with star", alice, ask(false, "*"), false, ""},
		{"anonymous with star", anon, ask(true, "*"), false, ""},
		{"anonymous in a named group", authz.Principal{Anonymous: true, Groups: []string{"/org/ds"}}, ask(true, "/org/ds"), false, ""},
		{"zero principal", authz.Principal{}, ask(true, "*"), false, ""},
		{"admin without the group", admin, ask(true, "/org/ds"), false, ""},
		{"admin with the group", adminInDS, ask(true, "/org/ds"), true, "group:/org/ds"},
		{"no connector", bob, nil, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := decide(tc.p, authz.ActUseConnector, authz.Object{Connector: tc.c})
			if d.Allowed != tc.want || d.Via != tc.via {
				t.Fatalf("got %+v", d)
			}
			// Every refusal reads the same, so it does not reveal that a
			// connector exists or why it was refused.
			if !d.Allowed && d.Code != reasons.MsgNotFound {
				t.Fatalf("code %s", d.Code)
			}
		})
	}
}
