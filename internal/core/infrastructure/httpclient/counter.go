package httpclient

import (
	"net/http"

	"quotes/internal/metrics"
)

// WrapCounted increments metrics.OutboundHTTPRequestsTotal once per RoundTrip.
// Sits between retry and base in the resilience stack so each attempt is
// counted separately — subtracting outbound_http_retries_total yields the
// number of logical requests from callers.
func WrapCounted(next http.RoundTripper, component string) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if component == "" {
		return next
	}
	return &countedTransport{next: next, component: component}
}

type countedTransport struct {
	next      http.RoundTripper
	component string
}

func (t *countedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	metrics.OutboundHTTPRequestsTotal.
		WithLabelValues(t.component, metrics.OutboundOutcome(status, err)).
		Inc()
	return resp, err
}
