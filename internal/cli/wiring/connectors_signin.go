//go:build !local

package wiring

import (
	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/config"
	"github.com/nebari-dev/javo/internal/connectors/oauth"
	connreg "github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	gwconnectors "github.com/nebari-dev/javo/internal/gateway/connectors"
	"github.com/nebari-dev/javo/internal/httpx"
	"github.com/nebari-dev/javo/internal/store"
)

// signIn is the sign-in at connectors' vendors, which is also how the vault
// refreshes and revokes tokens. Every request goes through the guarded
// client, with connectors.internalHosts allowed.
func signIn(c config.ConnectorsCfg, db *store.Store, seal tink.AEAD, clients *connreg.Registry, redirectURI string) (gwconnectors.SignIn, vault.OAuth) {
	s := oauth.New(oauth.Config{
		DB: db, Seal: seal, Clients: clients, HTTP: httpx.Guard{PublicOnly: true, Internal: c.InternalHosts}.Client(),
		InternalHosts: c.InternalHosts, RedirectURI: redirectURI,
	})
	return s, s
}
