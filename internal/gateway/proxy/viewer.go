package proxy

import (
	"net/http"
	"strings"

	"github.com/nebari-dev/javo/internal/authz"
	"github.com/nebari-dev/javo/internal/model"
)

// setViewer tells the app who is viewing it: the signed-in person opening
// it, who need not be its owner (a shared app, a preview). It first drops
// every header and trailer under model.ViewerHeaderPrefix, from the client
// or a template, so the app can trust what it gets; nobody signed in gets
// none.
//
// X-Tapas-Id is the person's stable, unique name (the owner value on apps);
// X-Tapas-User, -Email and -Name are the provider's username, email and
// display name, for display. X-Tapas-Groups lists the groups, comma
// separated. Every value is percent-encoded UTF-8 (viewerValue), so a comma
// in a group or a non-ASCII name arrives intact.
func setViewer(out *http.Request, who authz.Principal) {
	for _, h := range []http.Header{out.Header, out.Trailer} {
		for k := range h {
			if model.IsViewerHeader(k) {
				delete(h, k)
			}
		}
	}
	if who.Anonymous {
		return
	}
	groups := make([]string, len(who.Groups))
	for i, g := range who.Groups {
		groups[i] = viewerValue(g)
	}
	h, p := out.Header, model.ViewerHeaderPrefix
	h.Set(p+"Id", viewerValue(who.ID))
	h.Set(p+"User", viewerValue(who.Username))
	h.Set(p+"Email", viewerValue(who.Email))
	h.Set(p+"Name", viewerValue(who.DisplayName))
	h.Set(p+"Groups", strings.Join(groups, ","))
}

// viewerValue percent-encodes the bytes of s that a header value cannot
// carry as they are (controls, non-ASCII), and "%" and ",": any standard
// percent-decoder (decodeURIComponent, urllib.parse.unquote) gives s back,
// and printable ASCII other than those two reads unchanged.
func viewerValue(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '%' || c == ',' {
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
