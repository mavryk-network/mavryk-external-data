package httpclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func mkRedirectChain(t *testing.T, urls ...string) (*http.Request, []*http.Request) {
	t.Helper()
	reqs := make([]*http.Request, 0, len(urls))
	for _, u := range urls {
		r, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			t.Fatalf("build request %q: %v", u, err)
		}
		reqs = append(reqs, r)
	}
	return reqs[len(reqs)-1], reqs[:len(reqs)-1]
}

func TestSameHostRedirectPolicy(t *testing.T) {
	tests := []struct {
		name      string
		chain     []string
		wantErr   string
		wantAllow bool
	}{
		{
			name:      "first request is always allowed",
			chain:     []string{"https://api.example.com/v1"},
			wantAllow: true,
		},
		{
			name:      "same host and scheme",
			chain:     []string{"https://api.example.com/v1", "https://api.example.com/v2"},
			wantAllow: true,
		},
		{
			name:    "cross-host redirect",
			chain:   []string{"https://api.example.com/v1", "https://evil.example.net/v1"},
			wantErr: "cross-host redirect",
		},
		{
			name:    "same-host https to http downgrade",
			chain:   []string{"https://api.example.com/v1", "http://api.example.com/v1"},
			wantErr: "downgrade redirect",
		},
		{
			name:      "http to https upgrade is fine",
			chain:     []string{"http://localhost:8080/v1", "https://localhost:8080/v1"},
			wantAllow: true,
		},
		{
			name:      "http origin staying http",
			chain:     []string{"http://localhost:8080/v1", "http://localhost:8080/v2"},
			wantAllow: true,
		},
		{
			name:    "port change counts as a different host",
			chain:   []string{"https://api.example.com/v1", "https://api.example.com:8443/v1"},
			wantErr: "cross-host redirect",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, via := mkRedirectChain(t, tc.chain...)
			err := SameHostRedirectPolicy(req, via)
			if tc.wantAllow {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestSameHostRedirectPolicy_StopsAfterTenRedirects(t *testing.T) {
	chain := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		chain = append(chain, "https://api.example.com/v1")
	}
	req, via := mkRedirectChain(t, chain...)
	if err := SameHostRedirectPolicy(req, via); err == nil ||
		!strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("want the redirect-depth error, got %v", err)
	}
}

// End-to-end proof of what the policy protects: Go never strips a custom header
// on a redirect, so without the policy the API key would be re-sent over the
// plaintext hop. (httptest cannot serve both schemes on one host:port, so this
// exercises the cross-host arm; the same-host downgrade arm is covered by the
// table above.)
func TestSameHostRedirectPolicy_BlocksCredentialLeakToPlaintextHost(t *testing.T) {
	var plaintextHits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" {
			plaintextHits.Add(1)
		}
	}))
	defer plain.Close()

	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", plain.URL)
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer tls.Close()

	client := tls.Client()
	client.CheckRedirect = SameHostRedirectPolicy
	req, err := http.NewRequest(http.MethodGet, tls.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("x-api-key", "secret")

	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to a plaintext host must be refused")
	}
	if got := plaintextHits.Load(); got != 0 {
		t.Errorf("api key reached the plaintext server %d times", got)
	}
}
