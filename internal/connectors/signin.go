package connectors

import (
	"errors"
	"strings"
)

// Errors of connecting an account to a connector.
var (
	// ErrSignInExpired is a sign-in that cannot be finished: its state is
	// unknown, used, expired, someone else's, or the connector changed
	// since it started.
	ErrSignInExpired = errors.New("connectors: the sign-in expired, was already used, or is not yours")
	// ErrNeedsClient is a connector with no OAuth client, whose
	// authorization server does not let Tapas register one.
	ErrNeedsClient = errors.New("connectors: the connector needs an OAuth client")
)

// OAuthError is a refusal from a connector's authorization server, or a
// failure reaching it, by an OAuth error code only (such as invalid_grant).
// Nothing else the server sent is kept, so it is safe to show and log.
type OAuthError struct{ Code string }

func (e *OAuthError) Error() string { return "connectors: the authorization server answered " + e.Code }

// Codes of an OAuthError Tapas gives itself.
const (
	// CodeUnreachable: the server could not be reached or gave no usable
	// answer.
	CodeUnreachable = "server_unreachable"
	// CodeDiscovery: the server's metadata could not be found or is not
	// acceptable.
	CodeDiscovery = "metadata_not_found"
	// CodeNoPKCE: the server does not advertise PKCE with S256.
	CodeNoPKCE = "pkce_s256_not_supported"
	// CodeIssuer: the answer named another issuer (RFC 9207).
	CodeIssuer = "issuer_mismatch"
)

// NewOAuthError is the OAuthError for code as a server sent it: kept when
// it is a short word of lowercase letters and underscores, as the codes
// RFC 6749 defines are, and "server_error" otherwise.
func NewOAuthError(code string) *OAuthError {
	if code == "" || len(code) > 64 || strings.ContainsFunc(code, func(r rune) bool { return (r < 'a' || r > 'z') && r != '_' }) {
		code = "server_error"
	}
	return &OAuthError{Code: code}
}

// IsInvalidGrant reports whether err is the server refusing a grant for
// good (the person must connect again).
func IsInvalidGrant(err error) bool {
	var oe *OAuthError
	return errors.As(err, &oe) && oe.Code == "invalid_grant"
}
