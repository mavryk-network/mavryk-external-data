package logging

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeRequestID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"abc-123_ID.4", "abc-123_ID.4"},
		{"  trimmed  ", "trimmed"},
		{"", ""},
		{"has space", ""},
		{"bad\nnewline", ""},
		{strings.Repeat("a", 129), ""}, // over 128
		{strings.Repeat("a", 128), strings.Repeat("a", 128)},
	}
	for _, c := range cases {
		if got := sanitizeRequestID(c.in); got != c.want {
			t.Errorf("sanitizeRequestID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("no-op case: %q", got)
	}
	got := truncate(strings.Repeat("x", 300), 256)
	if len([]rune(got)) != 257 { // 256 bytes + ellipsis rune
		t.Errorf("truncated len = %d runes, want 257", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated must end with ellipsis")
	}
}

func TestRedactURL(t *testing.T) {
	u, _ := url.Parse("https://basenet.api.equiteez.com/v1/graphql?bypass=TOPSECRET&foo=bar")
	got := redactURL(u)
	if strings.Contains(got, "TOPSECRET") {
		t.Fatalf("secret leaked in redacted URL: %s", got)
	}
	if !strings.Contains(got, "bypass=REDACTED") {
		t.Errorf("bypass not redacted: %s", got)
	}
	if !strings.Contains(got, "foo=bar") {
		t.Errorf("non-sensitive param dropped: %s", got)
	}

	// No sensitive params → returned unchanged.
	u2, _ := url.Parse("https://x.io/a?foo=bar")
	if redactURL(u2) != u2.String() {
		t.Errorf("clean URL should be unchanged, got %s", redactURL(u2))
	}
}

func TestScrubErrorURL(t *testing.T) {
	u, _ := url.Parse("https://x.io/gql?bypass=TOPSECRET")
	inner := fmt.Errorf(`Get "%s": dial tcp: i/o timeout`, u.String())
	scrubbed := scrubErrorURL(inner, u, redactURL(u))

	if strings.Contains(scrubbed.Error(), "TOPSECRET") {
		t.Fatalf("secret leaked in scrubbed error: %s", scrubbed.Error())
	}
	if !strings.Contains(scrubbed.Error(), "REDACTED") {
		t.Errorf("expected REDACTED in error, got %s", scrubbed.Error())
	}
	if !errors.Is(scrubbed, inner) {
		t.Errorf("scrubbed error must still unwrap to the original for errors.Is/As")
	}
}
