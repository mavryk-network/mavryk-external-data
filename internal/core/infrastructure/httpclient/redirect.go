package httpclient

import (
	"fmt"
	"net/http"
)

// SameHostRedirectPolicy refuses redirects that change the host, and refuses
// an https→http downgrade even when the host is unchanged. Go strips only
// Authorization/Cookie headers, and only when the HOST changes — custom auth
// headers (x-cg-pro-api-key) and a ?bypass= query echoed in Location survive
// both a cross-host hop and a same-host scheme downgrade, so a redirect to
// http:// would put them on the wire in cleartext. API endpoints have no
// legitimate cross-host or downgrading redirects, so fail closed on both.
func SameHostRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if req.URL.Host != origin.Host {
		return fmt.Errorf("refusing cross-host redirect from %s to %s", origin.Host, req.URL.Host)
	}
	// Upgrades (http→https) are safe and stay allowed; only the downgrade,
	// which exposes credentials the client re-sends, is refused.
	if origin.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing https→%s downgrade redirect on %s", req.URL.Scheme, origin.Host)
	}
	return nil
}
