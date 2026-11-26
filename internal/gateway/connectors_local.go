//go:build local

package gateway

import "github.com/danielgtaylor/huma/v2"

// mountConnectors is the connector routes of a binary built with the tag
// local: none. `javo local` never turns connectors on, and the server
// refuses a config that does.
func mountConnectors(huma.API, Config) {}
