package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/gin-gonic/gin"
)

type stubLegacyRepo struct {
	rows  []repositories.LegacyQuoteRow
	err   error
	gotTk string
	gotSr string
	gotFr time.Time
	gotTo time.Time
	gotLm int
}

func (s *stubLegacyRepo) QueryWide(
	_ context.Context, token, source string, from, to time.Time, limit int,
) ([]repositories.LegacyQuoteRow, error) {
	s.gotTk, s.gotSr, s.gotFr, s.gotTo, s.gotLm = token, source, from, to, limit
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

func newLegacyEngine(repo LegacyQuoteFetcher) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	deps := LegacyQuotesDeps{
		Repo:        repo,
		TokenSymbol: "mvrk",
		SourceCode:  "coingecko",
		MaxLimit:    10000,
	}
	r.GET("/quotes", deps.LegacyQuotes())
	return r
}

func TestLegacyQuotes_WideShape(t *testing.T) {
	ts := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	repo := &stubLegacyRepo{
		rows: []repositories.LegacyQuoteRow{
			{Timestamp: ts, BTC: 6.1e-7, USD: 0.071541, EUR: 0.060941, GBP: 0.053079},
		},
	}
	r := newLegacyEngine(repo)

	req := httptest.NewRequest(http.MethodGet,
		"/quotes?from=2026-04-27T00:00:00Z&to=2026-04-27T23:59:59Z&limit=100", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
	row := got[0]
	wantKeys := []string{"timestamp", "btc", "usd", "eur", "cny", "jpy", "krw", "eth", "gbp"}
	for _, k := range wantKeys {
		if _, ok := row[k]; !ok {
			t.Errorf("missing key %q in row: %v", k, row)
		}
	}
	if row["timestamp"] != "2026-04-27T12:00:00Z" {
		t.Errorf("timestamp = %v, want RFC3339-Z", row["timestamp"])
	}
	if row["usd"].(float64) != 0.071541 {
		t.Errorf("usd = %v, want 0.071541", row["usd"])
	}
	// Missing currencies decode as zero, matching legacy behaviour.
	if row["cny"].(float64) != 0 {
		t.Errorf("cny = %v, want 0 (legacy zero-fill)", row["cny"])
	}
	if repo.gotTk != "mvrk" || repo.gotSr != "coingecko" {
		t.Errorf("repo got (%q,%q), want (mvrk,coingecko)", repo.gotTk, repo.gotSr)
	}
	if repo.gotLm != 100 {
		t.Errorf("repo limit = %d, want 100", repo.gotLm)
	}
}

func TestLegacyQuotes_DefaultWindowIs24h(t *testing.T) {
	repo := &stubLegacyRepo{rows: nil}
	r := newLegacyEngine(repo)

	req := httptest.NewRequest(http.MethodGet, "/quotes", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "[]" {
		t.Errorf("empty body = %q, want []", w.Body.String())
	}
	delta := repo.gotTo.Sub(repo.gotFr)
	if delta < 23*time.Hour || delta > 25*time.Hour {
		t.Errorf("default window = %v, want ~24h", delta)
	}
}

func TestLegacyQuotes_BadFrom_400(t *testing.T) {
	r := newLegacyEngine(&stubLegacyRepo{})
	req := httptest.NewRequest(http.MethodGet, "/quotes?from=not-rfc3339", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["error"]; !ok {
		t.Errorf("legacy error envelope missing 'error' key: %v", body)
	}
}

func TestLegacyQuotes_FromAfterTo_400(t *testing.T) {
	r := newLegacyEngine(&stubLegacyRepo{})
	req := httptest.NewRequest(http.MethodGet,
		"/quotes?from=2026-04-27T12:00:00Z&to=2026-04-27T00:00:00Z", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLegacyQuotes_LimitExceedsMax_400(t *testing.T) {
	r := newLegacyEngine(&stubLegacyRepo{})
	req := httptest.NewRequest(http.MethodGet, "/quotes?limit=999999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLegacyQuotes_NegativeLimit_400(t *testing.T) {
	r := newLegacyEngine(&stubLegacyRepo{})
	req := httptest.NewRequest(http.MethodGet, "/quotes?limit=-1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLegacyQuotes_RepoError_500WithLegacyEnvelope(t *testing.T) {
	r := newLegacyEngine(&stubLegacyRepo{err: errors.New("db down")})
	req := httptest.NewRequest(http.MethodGet, "/quotes", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["error"]; !ok {
		t.Errorf("legacy 500 envelope missing 'error' key: %v", body)
	}
}

// TestLegacyQuotes_NoLimitFallsBackToServerCap pins the DoS fix: an absent
// ?limit must reach the repository as the server cap, never as 0 (which the
// repository would render as "no LIMIT" over the token's whole history).
func TestLegacyQuotes_NoLimitFallsBackToServerCap(t *testing.T) {
	repo := &stubLegacyRepo{}
	r := newLegacyEngine(repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/quotes", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if repo.gotLm != 10000 {
		t.Errorf("limit = %d, want 10000 (server cap)", repo.gotLm)
	}
}

func TestLegacyQuotes_WindowExceedingMaxSpan_400(t *testing.T) {
	repo := &stubLegacyRepo{}
	r := newLegacyEngine(repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/quotes?from=1970-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if repo.gotLm != 0 || !repo.gotFr.IsZero() {
		t.Error("repository must not be reached for an over-wide window")
	}
}

func TestLegacyQuotes_WindowWithinMaxSpan_OK(t *testing.T) {
	repo := &stubLegacyRepo{}
	r := newLegacyEngine(repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/quotes?from=2026-01-01T00:00:00Z&to=2026-03-01T00:00:00Z", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
}
