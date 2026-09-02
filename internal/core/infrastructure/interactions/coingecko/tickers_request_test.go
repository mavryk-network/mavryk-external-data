package coingecko

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rs/zerolog"
)

// Pins the GetTickers request shape: path-escaped coinID, host-aware API-key
// header, encoded include_exchange_logo query.
func TestGetTickers_RequestShape(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Mavryk","tickers":[]}`))
	}))
	defer srv.Close()

	nop := zerolog.Nop()
	c := &Client{baseURL: srv.URL + "/api/v3", apiKey: "k", http: srv.Client(), log: &nop}

	if _, err := c.GetTickers(t.Context(), "weird/id", true); err != nil {
		t.Fatalf("GetTickers: %v", err)
	}
	if got == nil {
		t.Fatal("no request reached the fake upstream")
	}
	wantPath := "/api/v3/coins/" + url.PathEscape("weird/id") + "/tickers"
	if got.URL.EscapedPath() != wantPath {
		t.Errorf("path = %q, want %q (coinID must be path-escaped)", got.URL.EscapedPath(), wantPath)
	}
	if got.URL.Query().Get("include_exchange_logo") != "true" {
		t.Errorf("query = %q, want include_exchange_logo=true", got.URL.RawQuery)
	}
	// httptest host is neither the demo nor the pro CoinGecko host, so the
	// host-aware helper must fall back to the demo header, never the pro one.
	if got.Header.Get("x-cg-pro-api-key") != "" {
		t.Error("pro header must not be sent to a non-pro host")
	}
	if got.Header.Get("x-cg-demo-api-key") == "" {
		t.Error("demo header missing on non-pro host")
	}
}
