package mvkt

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newHistoryClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	s := httptest.NewServer(handler)
	return &Client{baseURL: s.URL + "/v1", http: s.Client()}, s.Close
}
