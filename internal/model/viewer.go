package model

import "strings"

// ViewerHeaderPrefix starts the name of every request header the gateway
// sets to tell an app who is viewing it (X-Tapas-User and the rest). Only the
// gateway sets them: whatever a client or a template sends under the prefix
// is dropped, so an app can trust them.
const ViewerHeaderPrefix = "X-Tapas-"

// IsViewerHeader is true for a header name under ViewerHeaderPrefix in any
// case, also with "_" for "-": servers that read headers CGI-style (WSGI,
// Rack, PHP) see X_Tapas_User as X-Tapas-User.
func IsViewerHeader(name string) bool {
	n := strings.ReplaceAll(name, "_", "-")
	return len(n) >= len(ViewerHeaderPrefix) && strings.EqualFold(n[:len(ViewerHeaderPrefix)], ViewerHeaderPrefix)
}
