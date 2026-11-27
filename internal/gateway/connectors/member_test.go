package connectors_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/connectors/connectorstest"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

type myConnector struct {
	ID, Slug, Name, Status, ConnectURL, ConnectedAt, CatalogID string
	Connected                                                  bool
	Tools                                                      []struct{ Name, Mode string }
}

type adminView struct {
	adminConnector
	Connections int
	Tools       []struct{ Name string }
}

// oauthFixture is a gateway with connectors on, reaching a fake connector
// and its fake authorization server on loopback.
type oauthFixture struct {
	*apitest.Fixture
	as    *connectorstest.AuthServer
	mcp   *connectorstest.Connector
	admin *http.Cookie
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	f := apitest.New(t, apitest.Options{
		Connectors: true, PublicURL: "https://tapas.example.org", ConnectorInternalHosts: []string{"127.0.0.1"},
		ConnectorHTTP: httpx.Guard{}.Client(),
	})
	as := connectorstest.NewAuthServer(t)
	return &oauthFixture{Fixture: f, as: as, mcp: connectorstest.NewOAuthConnector(t, as), admin: f.Session(apitest.AdminID, "javo-admins")}
}

// create adds a connector at the fake, for the sales group, and returns
// its id.
func (f *oauthFixture) create(t *testing.T) string {
	t.Helper()
	body := fmt.Sprintf(`{"slug":"fake","name":"Fake","url":%q,"enabled":true,
		"oauth":{"clientId":%q,"clientSecret":%q},"allowedGroups":["sales"],"toolModes":{"echo":"read"}}`,
		f.mcp.URL, connectorstest.ClientID, connectorstest.ClientSecret)
	w := f.Do("POST", "/api/admin/connectors", body, f.admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	return apitest.Decode[adminConnector](t, w).ID
}

// start begins a sign-in at path with c and follows it at the fake vendor,
// returning the body the callback page posts.
func (f *oauthFixture) start(t *testing.T, path string, c *http.Cookie) string {
	t.Helper()
	w := f.Do("POST", path, "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s: %d %s", path, w.Code, w.Body)
	}
	u := apitest.Decode[struct{ AuthorizeURL string }](t, w).AuthorizeURL
	if q := mustQuery(t, u); q.Get("redirect_uri") != "https://tapas.example.org/settings/connections/callback/fake" {
		t.Fatalf("redirect_uri %q", q.Get("redirect_uri"))
	}
	back, err := f.as.Authorize(u)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"slug":"fake","state":%q,"code":%q,"iss":%q}`, back.Get("state"), back.Get("code"), back.Get("iss"))
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func (f *oauthFixture) mine(t *testing.T, c *http.Cookie) []myConnector {
	t.Helper()
	w := f.Do("GET", "/api/connectors", "", c)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	return apitest.Decode[struct{ Items []myConnector }](t, w).Items
}

// A member's list says which catalogue entry each connector was made from,
// so the page can show its vendor's logo without the admin-only catalogue.
func TestAMembersListNamesEachConnectorsCatalogueEntry(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	body := fmt.Sprintf(`{"catalogId":"github","slug":"gh","name":"Code","url":%q,"enabled":true,
		"oauth":{"clientId":%q,"clientSecret":%q},"allowedGroups":["sales"]}`, f.mcp.URL, connectorstest.ClientID, connectorstest.ClientSecret)
	if w := f.Do("POST", "/api/admin/connectors", body, f.admin); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	f.create(t)
	got := map[string]string{}
	for _, c := range f.mine(t, f.Session(apitest.AliceID, "sales")) {
		got[c.Slug] = c.CatalogID
	}
	if got["gh"] != "github" || got["fake"] != "" || len(got) != 2 {
		t.Fatalf("catalogue ids %v", got)
	}
}

// A person connects their own account; someone else cannot finish their
// sign-in, which spends it; they disconnect, which revokes at the vendor.
func TestAPersonConnectsAndDisconnects(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	alice, carol := f.Session(apitest.AliceID, "sales"), f.Session(apitest.MemberID, "sales")

	if got := f.mine(t, alice); len(got) != 1 || got[0].Connected || got[0].ConnectURL != "https://tapas.example.org/settings/connections" {
		t.Fatalf("before connecting: %+v", got)
	}
	body := f.start(t, "/api/connectors/"+id+"/connect", alice)
	expired := reasons.Msg(reasons.MsgConnectorSignInExpired)
	for _, c := range []*http.Cookie{carol, alice} {
		if w := f.Do("POST", "/api/connectors/oauth/complete", body, c); w.Code != http.StatusBadRequest ||
			apitest.Decode[problemJSON](t, w).Title != expired {
			t.Fatalf("completing a sign-in someone else tried first: %d %s", w.Code, w.Body)
		}
	}
	body = f.start(t, "/api/connectors/"+id+"/connect", alice)
	w := f.Do("POST", "/api/connectors/oauth/complete", body, alice)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	if c := apitest.Decode[struct{ Connector myConnector }](t, w).Connector; !c.Connected || c.Status != "active" || c.ConnectedAt == "" {
		t.Fatalf("completed %+v", c)
	}
	if got := f.mine(t, alice); !got[0].Connected {
		t.Fatalf("alice's list after connecting: %+v", got)
	}
	if got := f.mine(t, carol); got[0].Connected {
		t.Fatalf("carol sees alice's connection: %+v", got)
	}
	if v := apitest.Decode[adminView](t, f.Do("GET", "/api/admin/connectors/"+id, "", f.admin)); v.Connections != 1 {
		t.Fatalf("admin view %+v", v)
	}

	if w := f.Do("DELETE", "/api/connectors/"+id+"/connection", "", alice); w.Code != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", w.Code, w.Body)
	}
	if r := f.as.Revocations(); len(r) != 2 || r[0].Hint != "refresh_token" || r[1].Hint != "access_token" {
		t.Fatalf("revocations %+v", r)
	}
	if got := f.mine(t, alice); got[0].Connected {
		t.Fatalf("after disconnecting: %+v", got)
	}
	if w := f.Do("DELETE", "/api/connectors/"+id+"/connection", "", alice); w.Code != http.StatusNotFound {
		t.Fatalf("second disconnect: %d %s", w.Code, w.Body)
	}
}

// Once a connector's MCP server names another authorization server than the
// one pinned at the first sign-in, nobody can start a sign-in, and the admin
// sees which server is pinned and that it moved.
func TestNoSignInStartsAfterTheAuthorizationServerMoved(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	alice := f.Session(apitest.AliceID, "sales")
	if w := f.Do("POST", "/api/connectors/oauth/complete", f.start(t, "/api/connectors/"+id+"/connect", alice), alice); w.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	if err := f.Db.Connectors().AuthServerMoved(context.Background(), model.Actor{System: model.SystemGateway}, id, f.as.URL); err != nil {
		t.Fatal(err)
	}
	w := f.Do("POST", "/api/connectors/"+id+"/connect", "", alice)
	if w.Code != http.StatusConflict || apitest.Decode[problemJSON](t, w).Title != reasons.Msg(reasons.MsgConnectorServerMoved) {
		t.Fatalf("connect after the move: %d %s", w.Code, w.Body)
	}
	v := apitest.Decode[struct {
		OAuth struct {
			Issuer      string
			ServerMoved bool
		}
	}](t, f.Do("GET", "/api/admin/connectors/"+id, "", f.admin))
	if v.OAuth.Issuer != f.as.URL || !v.OAuth.ServerMoved {
		t.Fatalf("admin view %+v", v.OAuth)
	}
}

// What a vendor sent back reaches the callback page of the connector the
// sign-in was for; the same answer posted from another connector's page is
// refused before the code is exchanged (OAuth mix-up).
func TestASignInFinishesOnlyFromItsConnectorsPage(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	alice := f.Session(apitest.AliceID, "sales")
	body := strings.Replace(f.start(t, "/api/connectors/"+id+"/connect", alice), `"slug":"fake"`, `"slug":"other"`, 1)
	w := f.Do("POST", "/api/connectors/oauth/complete", body, alice)
	if w.Code != http.StatusBadGateway || !strings.Contains(apitest.Decode[problemJSON](t, w).Title, "redirect_mismatch") {
		t.Fatalf("complete from another page: %d %s", w.Code, w.Body)
	}
	if f.as.Exchanges() != 0 {
		t.Fatal("the code was exchanged")
	}
}

// Someone outside the connector's groups does not see it and cannot
// connect it.
func TestOutsidersCannotConnect(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	carol := f.Session(apitest.MemberID, "marketing")
	if got := f.mine(t, carol); len(got) != 0 {
		t.Fatalf("an outsider lists %+v", got)
	}
	notFound := reasons.Msg(reasons.MsgConnectorNotFound)
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/connectors/" + id + "/connect"}, {"DELETE", "/api/connectors/" + id + "/connection"}, {"POST", "/api/connectors/c-missing/connect"},
	} {
		if w := f.Do(r.method, r.path, "", carol); w.Code != http.StatusNotFound || apitest.Decode[problemJSON](t, w).Title != notFound {
			t.Errorf("%s %s: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
}

// An admin takes a new snapshot of a connector's tools with their own
// connection: before connecting there is none to list them with. A member
// cannot.
func TestAnAdminRefreshesToolsWithTheirOwnConnection(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	alice := f.Session(apitest.AliceID, "sales")
	admin := f.Session(apitest.AdminID, "javo-admins", "sales")
	refresh := "/api/admin/connectors/" + id + "/tools/refresh"
	if w := f.Do("POST", refresh, "", alice); w.Code != http.StatusForbidden {
		t.Fatalf("a member refreshes: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", refresh, "", admin); w.Code != http.StatusConflict {
		t.Fatalf("refresh before connecting: %d %s", w.Code, w.Body)
	}
	if w := f.Do("POST", "/api/connectors/oauth/complete", f.start(t, "/api/connectors/"+id+"/connect", admin), admin); w.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	w := f.Do("POST", refresh, "", admin)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	if v := apitest.Decode[adminView](t, w); len(v.Tools) != 2 || v.Connections != 1 {
		t.Fatalf("after the refresh %+v", v)
	}
	// alice sees the tools, but is not connected by the admin's account.
	if got := f.mine(t, alice); len(got) != 1 || got[0].Connected || len(got[0].Tools) != 2 || got[0].Tools[0].Name != "fake__echo" || got[0].Tools[0].Mode != "read" {
		t.Fatalf("alice's list %+v", got)
	}
}

// Deleting a connector revokes and deletes what people connected.
func TestDeletingAConnectorRevokesItsConnections(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	alice := f.Session(apitest.AliceID, "sales")
	if w := f.Do("POST", "/api/connectors/oauth/complete", f.start(t, "/api/connectors/"+id+"/connect", alice), alice); w.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", w.Code, w.Body)
	}
	if w := f.Do("DELETE", "/api/admin/connectors/"+id, "", f.admin); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if len(f.as.Revocations()) != 2 {
		t.Fatalf("revocations %+v", f.as.Revocations())
	}
	if left, err := f.Vault.ForConnector(context.Background(), id); err != nil || len(left) != 0 {
		t.Fatalf("connections left: %+v, %v", left, err)
	}
}

// No API token connects, disconnects or finishes a sign-in; a token may
// list connectors.
func TestSignInRoutesAreForBrowserSessionsOnly(t *testing.T) {
	t.Parallel()
	f := newOAuthFixture(t)
	id := f.create(t)
	tok := f.NewToken(f.LoginAlice(), `{"name":"agent","scope":"read-write"}`).Token
	only := reasons.Msg(reasons.MsgConnectorSessionOnly)
	for _, r := range []struct{ method, path, body string }{
		{"POST", "/api/connectors/" + id + "/connect", ""},
		{"POST", "/api/connectors/oauth/complete", `{"slug":"fake","state":"s","code":"c"}`},
		{"DELETE", "/api/connectors/" + id + "/connection", ""},
	} {
		if w := f.Bearer(r.method, r.path, r.body, tok, nil); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), only) {
			t.Errorf("%s %s with a token: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
	if w := f.Bearer("GET", "/api/connectors", "", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("list with a token: %d %s", w.Code, w.Body)
	}
}

func TestMemberRoutesAnswerConnectorsOffWhenOff(t *testing.T) {
	t.Parallel()
	f := fixture(t, false)
	alice := f.Session(apitest.AliceID, "sales")
	admin := f.Session(apitest.AdminID, "javo-admins")
	for _, r := range []struct {
		method, path, body string
		c                  *http.Cookie
	}{
		{"GET", "/api/connectors", "", alice},
		{"POST", "/api/connectors/c-1/connect", "", alice},
		{"POST", "/api/connectors/oauth/complete", `{"slug":"fake","state":"s","code":"c"}`, alice},
		{"DELETE", "/api/connectors/c-1/connection", "", alice},
		{"POST", "/api/admin/connectors/c-1/tools/refresh", "", admin},
	} {
		w := f.Do(r.method, r.path, r.body, r.c)
		if w.Code != http.StatusConflict || apitest.Decode[problemJSON](t, w).Title != reasons.Msg(reasons.MsgConnectorsOff) {
			t.Errorf("%s %s while off: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
}
