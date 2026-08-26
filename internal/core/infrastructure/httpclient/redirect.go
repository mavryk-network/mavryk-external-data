package httpclient

import (
	"fmt"
	"net/http"
)

// SameHostRedirectPolicy refuses redirects that change the host. Go strips
// only Authorization/Cookie headers on cross-domain redirects — custom auth
// headers (x-cg-pro-api-key, ?bypass= query) would follow to any host an
// upstream (or an on-path attacker on an http hop) points at. API endpoints
// have no legitimate cross-host redirects, so fail closed.
func SameHostRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if len(via) > 0 && req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("refusing cross-host redirect from %s to %s", via[0].URL.Host, req.URL.Host)
	}
	return nil
}
