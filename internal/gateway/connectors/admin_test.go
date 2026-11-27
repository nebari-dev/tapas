package connectors_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/reasons"
)

const secret = "client-secret-sentinel-5d1c"

type adminConnector struct {
	ID, Rev, CatalogID, Slug, Name, URL string
	Enabled                             bool
	OAuth                               struct {
		ClientID, Registration, AuthorizeURL, TokenURL string
		SecretStored                                   bool
	}
	Scopes, AllowedGroups []string
	ToolModes             map[string]string
	RedirectURI           string
}

type problemJSON struct {
	Title  string
	Errors []struct{ Location, Message string }
}

func fixture(t *testing.T, on bool) *apitest.Fixture {
	t.Helper()
	return apitest.New(t, apitest.Options{Connectors: on, PublicURL: "https://tapas.example.org"})
}

const fakeBody = `{"slug":"fake","name":"Fake","url":"https://mcp.example.org/mcp","enabled":true,
	"oauth":{"clientId":"client-1","clientSecret":"` + secret + `"},"scopes":["read"],"allowedGroups":["sales"],
	"toolModes":{"echo":"read","post":"write"}}`

// A member is refused every admin route, and nothing is stored.
func TestAMemberGets403OnEveryAdminRoute(t *testing.T) {
	t.Parallel()
	f := fixture(t, true)
	member := f.Session(apitest.MemberID)
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/admin/connectors/catalog", ""},
		{"GET", "/api/admin/connectors", ""},
		{"POST", "/api/admin/connectors", fakeBody},
		{"GET", "/api/admin/connectors/c-1", ""},
		{"PUT", "/api/admin/connectors/c-1", fakeBody},
		{"DELETE", "/api/admin/connectors/c-1", ""},
	} {
		if w := f.Do(r.method, r.path, r.body, member); w.Code != http.StatusForbidden {
			t.Errorf("%s %s by a member: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
	if all, err := f.Db.Connectors().List(context.Background()); err != nil || len(all) != 0 {
		t.Fatalf("a member's create was stored: %v, %v", all, err)
	}
}

func TestRoutesAnswerConnectorsOffWhenOff(t *testing.T) {
	t.Parallel()
	f := fixture(t, false)
	admin := f.Session(apitest.AdminID, "javo-admins")
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/api/admin/connectors/catalog", ""},
		{"GET", "/api/admin/connectors", ""},
		{"POST", "/api/admin/connectors", fakeBody},
		{"GET", "/api/admin/connectors/c-1", ""},
		{"PUT", "/api/admin/connectors/c-1", fakeBody},
		{"DELETE", "/api/admin/connectors/c-1", ""},
	} {
		w := f.Do(r.method, r.path, r.body, admin)
		if w.Code != http.StatusConflict || apitest.Decode[problemJSON](t, w).Title != reasons.Msg(reasons.MsgConnectorsOff) {
			t.Errorf("%s %s while off: %d %s", r.method, r.path, w.Code, w.Body)
		}
	}
}

// An admin creates, reads, lists, changes and deletes a connector; the
// client secret goes in and never comes out.
func TestAdminManagesAConnector(t *testing.T) {
	t.Parallel()
	f := fixture(t, true)
	admin := f.Session(apitest.AdminID, "javo-admins")
	w := f.Do("POST", "/api/admin/connectors", fakeBody, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	c := apitest.Decode[adminConnector](t, w)
	if c.ID == "" || c.Slug != "fake" || !c.OAuth.SecretStored || c.OAuth.Registration != "static" || c.ToolModes["echo"] != "read" ||
		c.RedirectURI != "https://tapas.example.org/settings/connections/callback/fake" {
		t.Fatalf("created %+v", c)
	}
	bodies := []string{w.Body.String()}
	got := f.Do("GET", "/api/admin/connectors/"+c.ID, "", admin)
	list := f.Do("GET", "/api/admin/connectors", "", admin)
	if got.Code != http.StatusOK || list.Code != http.StatusOK || !strings.Contains(list.Body.String(), c.ID) {
		t.Fatalf("get %d %s; list %d %s", got.Code, got.Body, list.Code, list.Body)
	}
	bodies = append(bodies, got.Body.String(), list.Body.String())

	put := strings.Replace(strings.Replace(fakeBody, `"clientSecret":"`+secret+`"`, `"clientSecret":""`, 1), `"name":"Fake"`, `"name":"Renamed","rev":"`+c.Rev+`"`, 1)
	w = f.Do("PUT", "/api/admin/connectors/"+c.ID, put, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	if u := apitest.Decode[adminConnector](t, w); u.Name != "Renamed" || !u.OAuth.SecretStored || u.Rev == c.Rev {
		t.Fatalf("updated %+v", u)
	}
	bodies = append(bodies, w.Body.String(), f.Do("GET", "/api/admin/audit", "", admin).Body.String())
	// The same rev again: someone else saved first.
	if w := f.Do("PUT", "/api/admin/connectors/"+c.ID, put, admin); w.Code != http.StatusConflict ||
		apitest.Decode[problemJSON](t, w).Title != reasons.Msg(reasons.MsgConnectorChanged) {
		t.Fatalf("stale rev: %d %s", w.Code, w.Body)
	}
	for _, b := range bodies {
		if strings.Contains(b, secret) {
			t.Fatalf("an answer holds the client secret: %s", b)
		}
	}

	if w := f.Do("DELETE", "/api/admin/connectors/"+c.ID, "", admin); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	for _, method := range []string{"GET", "DELETE"} {
		w := f.Do(method, "/api/admin/connectors/"+c.ID, "", admin)
		if w.Code != http.StatusNotFound || apitest.Decode[problemJSON](t, w).Title != reasons.Msg(reasons.MsgConnectorNotFound) {
			t.Errorf("%s after delete: %d %s", method, w.Code, w.Body)
		}
	}
}

func TestCreateFromACatalogueEntry(t *testing.T) {
	t.Parallel()
	f := fixture(t, true)
	admin := f.Session(apitest.AdminID, "javo-admins")
	w := f.Do("GET", "/api/admin/connectors/catalog", "", admin)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"google-calendar"`) {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	}
	// Each connector has its own redirect address: the prefix and its slug.
	if p := apitest.Decode[struct{ RedirectURIPrefix string }](t, w).RedirectURIPrefix; p != "https://tapas.example.org/settings/connections/callback/" {
		t.Fatalf("redirect prefix %q", p)
	}
	w = f.Do("POST", "/api/admin/connectors", `{"catalogId":"google-calendar","oauth":{"clientId":"client-1","clientSecret":"`+secret+`"},"allowedGroups":["*"]}`, admin)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	c := apitest.Decode[adminConnector](t, w)
	if c.CatalogID != "google-calendar" || c.Slug != "gcalendar" || c.URL == "" || c.OAuth.TokenURL == "" || len(c.Scopes) == 0 || c.Enabled {
		t.Fatalf("created %+v", c)
	}
}

func TestDuplicateSlugIs409AndBadURLIs422(t *testing.T) {
	t.Parallel()
	f := fixture(t, true)
	admin := f.Session(apitest.AdminID, "javo-admins")
	if w := f.Do("POST", "/api/admin/connectors", fakeBody, admin); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w := f.Do("POST", "/api/admin/connectors", fakeBody, admin)
	if w.Code != http.StatusConflict || apitest.Decode[problemJSON](t, w).Title != reasons.Msg(reasons.MsgConnectorSlugTaken, "fake") {
		t.Fatalf("duplicate slug: %d %s", w.Code, w.Body)
	}
	bad := strings.Replace(strings.Replace(fakeBody, `"slug":"fake"`, `"slug":"other"`, 1), "https://mcp.example.org", "http://mcp.example.org", 1)
	w = f.Do("POST", "/api/admin/connectors", bad, admin)
	p := apitest.Decode[problemJSON](t, w)
	if w.Code != http.StatusUnprocessableEntity || len(p.Errors) != 1 || p.Errors[0].Location != "body.url" ||
		p.Errors[0].Message != reasons.Msg(reasons.MsgConnectorURLNotAllowed) {
		t.Fatalf("bad url: %d %s", w.Code, w.Body)
	}
}
