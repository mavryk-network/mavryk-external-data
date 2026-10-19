package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// The 2048-bit floor must hold on the JWKS path too, not just the local-key
// branch.
func TestMinRSABitsKeyfunc(t *testing.T) {
	tok := &jwt.Token{Header: map[string]interface{}{"kid": "test-kid"}}

	t.Run("weak RSA key refused", func(t *testing.T) {
		weak, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // deliberately weak: the test asserts rejection
		if err != nil {
			t.Fatal(err)
		}
		kf := minRSABitsKeyfunc(func(*jwt.Token) (interface{}, error) { return &weak.PublicKey, nil })
		if _, err := kf(tok); err == nil || !strings.Contains(err.Error(), "2048") {
			t.Fatalf("1024-bit key: err = %v, want a 2048-floor refusal", err)
		}
	})

	t.Run("2048-bit key passes", func(t *testing.T) {
		strong, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		kf := minRSABitsKeyfunc(func(*jwt.Token) (interface{}, error) { return &strong.PublicKey, nil })
		key, err := kf(tok)
		if err != nil {
			t.Fatalf("2048-bit key refused: %v", err)
		}
		if key != &strong.PublicKey {
			t.Fatal("key must pass through unchanged")
		}
	})

	t.Run("inner error passes through", func(t *testing.T) {
		kf := minRSABitsKeyfunc(func(*jwt.Token) (interface{}, error) { return nil, jwt.ErrTokenUnverifiable })
		if _, err := kf(tok); err == nil {
			t.Fatal("inner keyfunc error must propagate")
		}
	})
}
