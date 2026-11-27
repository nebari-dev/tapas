package reasons

import (
	"strings"
	"testing"
)

// Every connector message is in the catalog, and those that take arguments
// show them. Several are first emitted by later work on connectors (sign-in,
// the broker); they are all added here at once so that work does not edit
// this file.
func TestConnectorMessagesAreInCatalogAndTakeTheirArguments(t *testing.T) {
	plain := []MessageCode{
		MsgConnectorNotFound, MsgConnectorsOff, MsgConnectorURLNotAllowed, MsgConnectorSecretForNewAddress,
		MsgConnectorSignInExpired, MsgConnectorNeedsOAuthClient, MsgConnectorToolNotFound, MsgConnectorNotForAppTokens,
		MsgConnectorSessionOnly, MsgConnectorChanged, MsgConnectorSlugInvalid, MsgConnectorNameInvalid,
		MsgConnectorScopesInvalid, MsgConnectorGroupsInvalid, MsgConnectorToolModesInvalid,
		MsgConnectorEndpointsPaired, MsgConnectorCatalogUnknown, MsgConnectorServerMoved,
	}
	for _, c := range plain {
		if Msg(c) == Msg(MsgInvalidRequest) {
			t.Errorf("%s is not in the catalog", c)
		}
		if strings.Contains(Msg(c), "%") {
			t.Errorf("%s takes no argument but has a verb: %q", c, Msg(c))
		}
	}
	withArgs := []struct {
		code MessageCode
		args []any
	}{
		{MsgConnectorSlugTaken, []any{"slack"}},
		{MsgConnectorNotConnected, []any{"Slack", "https://tapas.example.org/settings/connections"}},
		{MsgConnectorSignInFailed, []any{"access_denied"}},
		{MsgConnectorDeniedByPolicy, []any{"writes to Slack need approval on weekends"}},
	}
	for _, c := range withArgs {
		got := Msg(c.code, c.args...)
		if got == Msg(MsgInvalidRequest) || strings.Contains(got, "%!") {
			t.Errorf("%s = %q", c.code, got)
		}
		for _, a := range c.args {
			if !strings.Contains(got, a.(string)) {
				t.Errorf("%s text %q lacks argument %q", c.code, got, a)
			}
		}
	}
}
