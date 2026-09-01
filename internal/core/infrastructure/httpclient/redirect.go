package httpclient

import (
	"fmt"
	"net/http"
)

// SameHostRedirectPolicy refuses a host change and an https→http downgrade. Go
// strips Authorization/Cookie only when the HOST changes, so custom auth headers
// and a ?bypass= echoed in Location would otherwise reach a cleartext hop. API
// endpoints have no legitimate cross-host or downgrading redirects.
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
	// Upgrades (http→https) stay allowed; only the downgrade is refused.
	if origin.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing https→%s downgrade redirect on %s", req.URL.Scheme, origin.Host)
	}
	return nil
}
