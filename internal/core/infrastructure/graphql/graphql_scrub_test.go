package graphql

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

var errBreakerOpen = errors.New("circuit breaker is open")

func TestExecute_DoErrorDoesNotLeakBypassSecret(t *testing.T) {
	client := &http.Client{Transport: failingTransport{err: errBreakerOpen}}
	_, err := Execute(context.Background(), client, "equiteez",
		"https://indexer.example.com/v1/graphql?bypass=SUPERSECRET", "query {}", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("secret leaked into error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "bypass=REDACTED") {
		t.Fatalf("expected redacted marker in error, got: %q", err.Error())
	}
	if !errors.Is(err, errBreakerOpen) {
		t.Fatalf("scrub must preserve the errors.Is chain, got: %v", err)
	}
}

func TestExecute_DoErrorWithoutSecretsUntouched(t *testing.T) {
	client := &http.Client{Transport: failingTransport{err: errBreakerOpen}}
	_, err := Execute(context.Background(), client, "svc",
		"https://api.example.com/v1/graphql", "query {}", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var scrubbed *scrubbedError
	if errors.As(err, &scrubbed) {
		t.Fatalf("no-secret URL must not be wrapped, got scrubbedError: %v", err)
	}
}

// A same-host redirect can embed a hop-2 URL the exact-URL replacement never
// matches — the secret VALUE must be scrubbed regardless of spelling.
func TestScrubURLError_RedirectedURLValueStillScrubbed(t *testing.T) {
	u, err := url.Parse("https://indexer.example.com/v1/graphql?bypass=SUPERSECRET")
	if err != nil {
		t.Fatal(err)
	}
	base := errors.New(`Get "https://indexer.example.com/v1/graphql/?bypass=SUPERSECRET": context deadline exceeded`)
	scrubbedErr := scrubURLError(base, u)
	if strings.Contains(scrubbedErr.Error(), "SUPERSECRET") {
		t.Fatalf("secret leaked through redirected-URL error: %q", scrubbedErr.Error())
	}
	if !errors.Is(scrubbedErr, base) {
		t.Fatalf("scrub must preserve the errors.Is chain, got: %v", scrubbedErr)
	}
}
