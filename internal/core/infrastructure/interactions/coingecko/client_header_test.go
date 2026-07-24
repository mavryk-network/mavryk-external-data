package coingecko

import (
	"net/http"
	"testing"
)

func TestSetAPIKeyHeader(t *testing.T) {
	t.Run("demo host uses demo header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		(&Client{baseURL: "https://api.coingecko.com/api/v3", apiKey: "k"}).setAPIKeyHeader(req)
		if got := req.Header.Get("x-cg-demo-api-key"); got != "k" {
			t.Errorf("demo header = %q, want k", got)
		}
		if req.Header.Get("x-cg-pro-api-key") != "" {
			t.Error("pro header must not be set on demo host")
		}
	})

	t.Run("pro host uses pro header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		(&Client{baseURL: "https://pro-api.coingecko.com/api/v3", apiKey: "k"}).setAPIKeyHeader(req)
		if got := req.Header.Get("x-cg-pro-api-key"); got != "k" {
			t.Errorf("pro header = %q, want k", got)
		}
	})

	t.Run("no key sets no header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
		(&Client{baseURL: "https://api.coingecko.com", apiKey: ""}).setAPIKeyHeader(req)
		if len(req.Header) != 0 {
			t.Errorf("expected no headers, got %v", req.Header)
		}
	})
}
