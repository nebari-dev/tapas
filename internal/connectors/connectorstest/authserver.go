package connectorstest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The client every AuthServer knows from the start, as an admin would
// paste it.
const (
	ClientID     = "client-1"
	ClientSecret = "secret-1"
)

// Revocation is one call to the revocation endpoint.
type Revocation struct{ Token, Hint string }

// AuthServer is a fake OAuth authorization server (RFC 8414 metadata,
// authorization code with PKCE, refresh with rotation, RFC 7591
// registration and RFC 7009 revocation). Its tokens are what a Connector
// made with NewOAuthConnector accepts. Knobs turn features off and make it
// refuse.
type AuthServer struct {
	// URL is its issuer and base address.
	URL string

	mu        sync.Mutex
	opts      AuthServerOptions
	clients   map[string]string // id: secret ("" for none)
	codes     map[string]grant
	access    map[string]issued
	refresh   map[string]grant
	queries   []url.Values
	refreshes int
	exchanges int
	revoked   []Revocation
	registers int
	seq       int
	lifetime  time.Duration
	refuse    bool
	failRev   bool
}

// AuthServerOptions turns features of an AuthServer off.
type AuthServerOptions struct {
	// PlainOnly advertises PKCE "plain" only, never S256.
	PlainOnly bool
	// NoRegistration leaves out the registration endpoint.
	NoRegistration bool
	// NoRevocation leaves out the revocation endpoint.
	NoRevocation bool
	// NoIssParameter does not advertise, nor send, iss on the redirect
	// (RFC 9207).
	NoIssParameter bool
	// NoRotation keeps the refresh token on a refresh (none is returned).
	NoRotation bool
}

// grant is what a code or a refresh token stands for.
type grant struct {
	client, redirect, challenge, resource, scope string
}

type issued struct {
	resource string
	expires  time.Time
}

// NewAuthServer starts an authorization server with every feature on. It
// stops when the test ends.
func NewAuthServer(t *testing.T) *AuthServer { return NewAuthServerWith(t, AuthServerOptions{}) }

// NewAuthServerWith starts an authorization server with o.
func NewAuthServerWith(t *testing.T, o AuthServerOptions) *AuthServer {
	t.Helper()
	a := &AuthServer{
		opts: o, clients: map[string]string{ClientID: ClientSecret}, codes: map[string]grant{},
		access: map[string]issued{}, refresh: map[string]grant{}, lifetime: time.Hour,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.metadata)
	mux.HandleFunc("GET /authorize", a.authorize)
	mux.HandleFunc("POST /token", a.token)
	mux.HandleFunc("POST /register", a.register)
	mux.HandleFunc("POST /revoke", a.revoke)
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	a.URL = s.URL
	return a
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (a *AuthServer) metadata(w http.ResponseWriter, _ *http.Request) {
	m := map[string]any{
		"issuer": a.URL, "authorization_endpoint": a.URL + "/authorize", "token_endpoint": a.URL + "/token",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
	}
	if a.opts.PlainOnly {
		m["code_challenge_methods_supported"] = []string{"plain"}
	}
	if !a.opts.NoRegistration {
		m["registration_endpoint"] = a.URL + "/register"
	}
	if !a.opts.NoRevocation {
		m["revocation_endpoint"] = a.URL + "/revoke"
	}
	if !a.opts.NoIssParameter {
		m["authorization_response_iss_parameter_supported"] = true
	}
	writeJSON(w, http.StatusOK, m)
}

// authorize stands for the vendor's sign-in page: it checks the request and
// redirects back with a code, as if the person had agreed.
func (a *AuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queries = append(a.queries, q)
	if _, ok := a.clients[q.Get("client_id")]; !ok || q.Get("response_type") != "code" || q.Get("redirect_uri") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		http.Error(w, "PKCE S256 is required", http.StatusBadRequest)
		return
	}
	a.seq++
	code := "code-" + strconv.Itoa(a.seq)
	a.codes[code] = grant{
		client: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"),
		resource: q.Get("resource"), scope: q.Get("scope"),
	}
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := url.Values{"code": {code}, "state": {q.Get("state")}}
	if !a.opts.NoIssParameter {
		v.Set("iss", a.URL)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// Authorize follows authorizeURL as a person's browser would and returns
// the query the vendor sends them back with (code, state and iss), or an
// error when the request was refused.
func (a *AuthServer) Authorize(authorizeURL string) (url.Values, error) {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(authorizeURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		return nil, fmt.Errorf("authorize answered %s", res.Status)
	}
	back, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		return nil, err
	}
	return back.Query(), nil
}

// client is the client a token request authenticated as, by basic auth or
// form fields; false when it did not.
func (a *AuthServer) client(r *http.Request) (string, bool) {
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	want, ok := a.clients[id]
	return id, ok && want == secret
}

func (a *AuthServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	client, ok := a.client(r)
	if !ok {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	f := r.PostForm
	var g grant
	switch f.Get("grant_type") {
	case "authorization_code":
		a.exchanges++
		code := f.Get("code")
		g, ok = a.codes[code]
		delete(a.codes, code)
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || g.client != client || g.redirect != f.Get("redirect_uri") || g.resource != f.Get("resource") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
	case "refresh_token":
		a.refreshes++
		g, ok = a.refresh[f.Get("refresh_token")]
		if !ok || a.refuse || g.client != client || (f.Get("resource") != "" && f.Get("resource") != g.resource) {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		if !a.opts.NoRotation {
			delete(a.refresh, f.Get("refresh_token"))
		}
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	a.seq++
	access := "at-" + strconv.Itoa(a.seq)
	a.access[access] = issued{resource: g.resource, expires: time.Now().Add(a.lifetime)}
	answer := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int(a.lifetime.Seconds())}
	if g.scope != "" {
		answer["scope"] = g.scope
	}
	if f.Get("grant_type") == "authorization_code" || !a.opts.NoRotation {
		refresh := "rt-" + strconv.Itoa(a.seq)
		a.refresh[refresh] = g
		answer["refresh_token"] = refresh
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, answer)
}

func (a *AuthServer) register(w http.ResponseWriter, r *http.Request) {
	if a.opts.NoRegistration {
		http.NotFound(w, r)
		return
	}
	var meta map[string]any
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.registers++
	a.seq++
	id, secret := "dyn-"+strconv.Itoa(a.seq), "dyn-secret-"+strconv.Itoa(a.seq)
	a.clients[id] = secret
	meta["client_id"], meta["client_secret"] = id, secret
	meta["token_endpoint_auth_method"] = "client_secret_basic"
	writeJSON(w, http.StatusCreated, meta)
}

func (a *AuthServer) revoke(w http.ResponseWriter, r *http.Request) {
	if a.opts.NoRevocation {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked = append(a.revoked, Revocation{Token: r.PostForm.Get("token"), Hint: r.PostForm.Get("token_type_hint")})
	if a.failRev {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, ok := a.client(r); !ok {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	delete(a.access, r.PostForm.Get("token"))
	delete(a.refresh, r.PostForm.Get("token"))
	w.WriteHeader(http.StatusOK)
}

// Valid reports whether access is a live access token it issued for
// resource (any resource when it was issued without one).
func (a *AuthServer) Valid(access, resource string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.access[access]
	return ok && time.Now().Before(t.expires) && (t.resource == "" || t.resource == resource)
}

// Expire makes every access token it issued expire now.
func (a *AuthServer) Expire() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, t := range a.access {
		t.expires = time.Now()
		a.access[k] = t
	}
}

// RefuseRefresh makes every refresh answer invalid_grant while on.
func (a *AuthServer) RefuseRefresh(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refuse = on
}

// FailRevocation makes the revocation endpoint answer 503 while on.
func (a *AuthServer) FailRevocation(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failRev = on
}

// AuthorizeQueries are the authorization requests it saw, in order.
func (a *AuthServer) AuthorizeQueries() []url.Values {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.queries)
}

// Exchanges is how many authorization codes it was asked to exchange.
func (a *AuthServer) Exchanges() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.exchanges
}

// Refreshes is how many refresh requests it answered.
func (a *AuthServer) Refreshes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshes
}

// Registrations is how many clients registered themselves.
func (a *AuthServer) Registrations() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.registers
}

// Revocations are the revocation requests it saw, in order.
func (a *AuthServer) Revocations() []Revocation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.revoked)
}

// HasRefresh reports whether refresh is a refresh token it still honours.
func (a *AuthServer) HasRefresh(refresh string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.refresh[refresh]
	return ok
}

// SetLifetime sets how long the access tokens it issues from now on last.
func (a *AuthServer) SetLifetime(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lifetime = d
}
