//go:build local

package gateway

import gwconnectors "github.com/nebari-dev/javo/internal/gateway/connectors"

// connectorTools is nil: a binary built with the tag local reaches no
// connector.
func connectorTools(Config) gwconnectors.ToolLister { return nil }
