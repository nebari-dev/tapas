package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/session"
	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/features/exposuresubdomain"
	"github.com/nebari-dev/javo/internal/gateway/authn"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

// viewerHeaders records the X-Tapas-* headers the app received, under their
// lowercase name with "_" read as "-" (as CGI-style servers read them).
type viewerHeaders struct {
	mu  sync.Mutex
	got map[string][]string
}

func (v *viewerHeaders) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body) // trailers arrive after the body
	v.mu.Lock()
	defer v.mu.Unlock()
	v.got = map[string][]string{}
	for _, h := range []http.Header{r.Header, r.Trailer} {
		for k, vals := range h {
			if n := strings.ReplaceAll(strings.ToLower(k), "_", "-"); strings.HasPrefix(n, "x-tapas-") {
				v.got[n] = append(v.got[n], vals...)
			}
		}
	}
}

func (v *viewerHeaders) seen() map[string][]string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.got
}

// wantViewer fails unless the app got exactly the headers in want, one value each.
func wantViewer(t *testing.T, got map[string][]string, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("app got %v, want %v", got, want)
	}
	for k, w := range want {
		if vals := got[k]; len(vals) != 1 || vals[0] != w {
			t.Fatalf("%s = %q, want %q (all: %v)", k, vals, w, got)
		}
	}
}

// subdomainSetup is setup with apps on their own host, where shared apps
// open, and Resolve answering with the app at backend and headers.
func subdomainSetup(t *testing.T, backend http.Handler, headers http.Header) *fixture {
	t.Helper()
	f := setup(t, backend)
	u, _ := url.Parse(f.backend.URL)
	f.proxy = New(Config{
		Store: f.db, Sessions: f.store, Activity: f.activity,
		Exposure:    exposuresubdomain.New(config.URLs{Mode: "subdomain", UIHost: "javo.example.com", AppsDomain: "apps.example.com"}),
		Revalidator: authn.NewRevalidator(nil, f.store, nil, 0, nil),
		Resolve: func(context.Context, *model.App, *model.Template) (Backend, error) {
			return Backend{URL: u, Headers: headers}, nil
		},
	})
	return f
}

// openShared seeds bob's app "board" with the given sharing access and opens
// it as viewer (nobody when nil) with the extra request headers (and, when
// trailer is not nil, a POST with those trailers), returning the status.
func openShared(t *testing.T, f *fixture, access string, viewer *authz.Principal, hdr, trailer http.Header) int {
	t.Helper()
	ctx := context.Background()
	tpl := gatewaytest.Template("open", "streamlit")
	tpl.Spec.MaxSharing = model.AccessPublic
	app := gatewaytest.App("bob-bbbbbbbb", "board")
	app.Template = "open"
	app.Sharing = model.Sharing{Access: access}
	gatewaytest.Seed(t, f.db, []any{tpl, app})
	got, err := f.db.Apps().GetByOwnerName(ctx, "bob-bbbbbbbb", "board")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	if trailer != nil {
		r = httptest.NewRequest("POST", "/", strings.NewReader("body"))
		r.TransferEncoding = []string{"chunked"}
		r.Trailer = trailer
	}
	r.Host = got.ObjectName() + ".apps.example.com"
	for k, v := range hdr {
		r.Header[k] = v
	}
	if viewer != nil {
		ui, err := f.store.Create(ctx, session.Session{Principal: *viewer, Provider: "local", Kind: session.KindUI, RevalidateAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		tok, err := f.store.Create(ctx, session.Session{Principal: *viewer, Provider: "local", Kind: session.KindApp,
			Parent: session.ID(ui), Workspace: got.Namespace + "/" + got.ObjectName()})
		if err != nil {
			t.Fatal(err)
		}
		tg, _ := exposuresubdomain.New(config.URLs{Mode: "subdomain", AppsDomain: "apps.example.com"}).Match(r)
		r.AddCookie(&http.Cookie{Name: f.proxy.(*proxy).appCookie(tg).Name, Value: tok})
	}
	w := f.do(r)
	if w.Code != 200 {
		t.Logf("body: %s", w.Body)
	}
	return w.Code
}

var (
	alice = authz.Principal{
		ID: "alice-aaaaaaaa", Username: "alice", Email: "alice@example.com", DisplayName: "Alice Émile",
		Groups: []string{"/data", "eng,ops"},
	}
	carol = authz.Principal{ID: "carol-cccccccc", Username: "carol"}
)

// An app opened by someone who is not its owner is told who that viewer is,
// not who owns it.
func TestSignedInViewerIsNamedToTheApp(t *testing.T) {
	t.Parallel()
	v := &viewerHeaders{}
	f := subdomainSetup(t, v, nil)
	if code := openShared(t, f, model.AccessSignedIn, &alice, nil, nil); code != 200 {
		t.Fatalf("got %d", code)
	}
	got := v.seen()
	wantViewer(t, got, map[string]string{
		"x-tapas-id":     "alice-aaaaaaaa",
		"x-tapas-user":   "alice",
		"x-tapas-email":  "alice@example.com",
		"x-tapas-name":   "Alice %C3%89mile",
		"x-tapas-groups": "/data,eng%2Cops",
	})
	// Each group decodes back to the provider's value, a comma and all.
	var groups []string
	for _, g := range strings.Split(got["x-tapas-groups"][0], ",") {
		d, err := url.PathUnescape(g)
		if err != nil {
			t.Fatal(err)
		}
		groups = append(groups, d)
	}
	if strings.Join(groups, "|") != "/data|eng,ops" {
		t.Fatalf("groups decode to %q", groups)
	}
}

// Nobody signed in: the app gets no X-Tapas-* header at all, even ones the
// client wrote itself, as headers or as trailers after the body.
func TestAnonymousViewerGetsNoIdentity(t *testing.T) {
	t.Parallel()
	v := &viewerHeaders{}
	f := subdomainSetup(t, v, nil)
	forged := http.Header{"X-Tapas-User": {"alice"}, "X-Tapas-Groups": {"admins"}, "X_tapas_id": {"alice-aaaaaaaa"}}
	if code := openShared(t, f, model.AccessPublic, nil, forged, forged); code != 200 {
		t.Fatalf("got %d", code)
	}
	if got := v.seen(); len(got) != 0 {
		t.Fatalf("anonymous viewer: app got %v", got)
	}
}

// What the client sends under any X-Tapas- name, in any case or with "_" for
// "-", never reaches the app; the gateway's values replace it.
func TestForgedViewerHeadersAreReplaced(t *testing.T) {
	t.Parallel()
	v := &viewerHeaders{}
	f := subdomainSetup(t, v, nil)
	forged := http.Header{
		"X-Tapas-User": {"alice"}, "x-tapas-email": {"alice@example.com"}, "X_Tapas_Groups": {"admins"},
		"X-TAPAS-NAME": {"Alice"}, "X-Tapas-Id": {"alice-aaaaaaaa"}, "X-Tapas-Role": {"admin"},
	}
	if code := openShared(t, f, model.AccessSignedIn, &carol, forged, nil); code != 200 {
		t.Fatalf("got %d", code)
	}
	wantViewer(t, v.seen(), map[string]string{
		"x-tapas-id": "carol-cccccccc", "x-tapas-user": "carol", "x-tapas-email": "", "x-tapas-name": "", "x-tapas-groups": "",
	})
}

// A template's proxyHeaders cannot name the viewer either.
func TestTemplateHeadersCannotNameTheViewer(t *testing.T) {
	t.Parallel()
	v := &viewerHeaders{}
	f := subdomainSetup(t, v, http.Header{"X-Tapas-User": {"root"}, "X_tapas_email": {"root@example.com"}})
	if code := openShared(t, f, model.AccessSignedIn, &carol, nil, nil); code != 200 {
		t.Fatalf("got %d", code)
	}
	wantViewer(t, v.seen(), map[string]string{
		"x-tapas-id": "carol-cccccccc", "x-tapas-user": "carol", "x-tapas-email": "", "x-tapas-name": "", "x-tapas-groups": "",
	})
}
