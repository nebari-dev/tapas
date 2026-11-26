//go:build local

package wiring

import (
	"github.com/tink-crypto/tink-go/v2/tink"

	"github.com/nebari-dev/javo/internal/config"
	connreg "github.com/nebari-dev/javo/internal/connectors/registry"
	"github.com/nebari-dev/javo/internal/connectors/vault"
	gwconnectors "github.com/nebari-dev/javo/internal/gateway/connectors"
	"github.com/nebari-dev/javo/internal/store"
)

// signIn is none: a binary built with the tag local reaches no vendor (and
// refuses connectors.enabled before it gets here).
func signIn(config.ConnectorsCfg, *store.Store, tink.AEAD, *connreg.Registry, string) (gwconnectors.SignIn, vault.OAuth) {
	return nil, nil
}
