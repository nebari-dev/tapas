package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nebari-dev/javo/internal/connectors"
	"github.com/nebari-dev/javo/internal/connectors/vault"
)

// Revoke revokes token at c's revocation endpoint (RFC 7009); hint is
// refresh_token or access_token. vault.ErrNoRevocation when there is none.
func (s *Service) Revoke(ctx context.Context, c connectors.Connector, token, hint string) error {
	e, err := pinnedEndpoints(c)
	if err != nil {
		return err
	}
	if e.revoke == "" {
		return vault.ErrNoRevocation
	}
	secret, err := s.c.Clients.ClientSecret(ctx, c.ID)
	if err != nil {
		return err
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}}
	if secret == "" {
		form.Set("client_id", c.OAuth.ClientID)
	}
	ctx, cancel := context.WithTimeout(ctx, vault.RevokeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.revoke, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if secret != "" {
		// RFC 6749 section 2.3.1: both are form-encoded first.
		req.SetBasicAuth(url.QueryEscape(c.OAuth.ClientID), url.QueryEscape(secret))
	}
	res, err := s.c.HTTP.Do(req)
	if err != nil {
		return &connectors.OAuthError{Code: connectors.CodeUnreachable}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return nil
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&body)
	return connectors.NewOAuthError(body.Error)
}
