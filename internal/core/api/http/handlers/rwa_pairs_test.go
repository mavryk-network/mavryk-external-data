package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
)

// stubPairsLister satisfies handlers.RWAPairsLister. Returns the configured
// pairs (assumed already enabled + sorted, mirroring the repository contract).
type stubPairsLister struct {
	pairs []prices.RWAPair
	err   error
	calls int
}

func (s *stubPairsLister) EnabledRWAPairs(_ context.Context) ([]prices.RWAPair, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.pairs, nil
}

func newRWAPairsEngine(_ *testing.T, deps RWAPairsDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/pairs/rwa", deps.List())
	return r
}

// TestRWAPairs_Shape — happy path: response is an array of
// {base, quote, source} objects, all keys lowercase, no extra fields,
// order preserved from the repository.
func TestRWAPairs_Shape(t *testing.T) {
	lister := &stubPairsLister{
		pairs: []prices.RWAPair{
			{Source: prices.SourceEquiteez, BaseSymbol: "MARS1", QuoteSymbol: "USDT", Enabled: true},
			{Source: prices.SourceEquiteez, BaseSymbol: "TST", QuoteSymbol: "USDT", Enabled: true},
		},
	}
	deps := RWAPairsDeps{Lookup: lister}
	r := newRWAPairsEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/pairs/rwa", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if lister.calls != 1 {
		t.Errorf("lister.calls = %d, want 1", lister.calls)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2", len(rows))
	}
	for i, row := range rows {
		if len(row) != 3 {
			t.Errorf("row[%d] has %d keys, want exactly 3: %v", i, len(row), row)
		}
		for _, k := range []string{"base", "quote", "source"} {
			v, ok := row[k].(string)
			if !ok {
				t.Errorf("row[%d].%s is %T, want string", i, k, row[k])
				continue
			}
			if v == "" {
				t.Errorf("row[%d].%s is empty", i, k)
			}
		}
	}
	// Lowercased: BaseSymbol "MARS1" → "mars1".
	if rows[0]["base"] != "mars1" {
		t.Errorf("rows[0].base = %v, want mars1 (lowercased)", rows[0]["base"])
	}
	if rows[0]["quote"] != "usdt" {
		t.Errorf("rows[0].quote = %v, want usdt (lowercased)", rows[0]["quote"])
	}
	if rows[0]["source"] != "equiteez" {
		t.Errorf("rows[0].source = %v, want equiteez", rows[0]["source"])
	}
}

// TestRWAPairs_OrderPreserved — handler does not re-sort; it trusts the
// repository's ORDER BY. Verifies the wire order matches the input order.
func TestRWAPairs_OrderPreserved(t *testing.T) {
	lister := &stubPairsLister{
		pairs: []prices.RWAPair{
			{Source: prices.SourceEquiteez, BaseSymbol: "mars1", QuoteSymbol: "usdt", Enabled: true},
			{Source: prices.SourceEquiteez, BaseSymbol: "tst", QuoteSymbol: "eur", Enabled: true},
			{Source: prices.SourceEquiteez, BaseSymbol: "tst", QuoteSymbol: "usdt", Enabled: true},
		},
	}
	deps := RWAPairsDeps{Lookup: lister}
	r := newRWAPairsEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/pairs/rwa", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []struct{ base, quote string }{
		{"mars1", "usdt"},
		{"tst", "eur"},
		{"tst", "usdt"},
	}
	if len(rows) != len(want) {
		t.Fatalf("len = %d, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i]["base"] != w.base || rows[i]["quote"] != w.quote {
			t.Errorf("rows[%d] = (%v,%v), want (%s,%s)",
				i, rows[i]["base"], rows[i]["quote"], w.base, w.quote)
		}
	}
}

// TestRWAPairs_EmptyResult_200 — no pairs in DB → [] (200), not 404.
func TestRWAPairs_EmptyResult_200(t *testing.T) {
	deps := RWAPairsDeps{Lookup: &stubPairsLister{}}
	r := newRWAPairsEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/pairs/rwa", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if body != "[]" && body != "[]\n" {
		t.Errorf("body = %q, want []", body)
	}
}

// TestRWAPairs_RepoError_500 — repository failure propagates as 500
// via the standard Wrap error mapping.
func TestRWAPairs_RepoError_500(t *testing.T) {
	deps := RWAPairsDeps{Lookup: &stubPairsLister{err: errors.New("db down")}}
	r := newRWAPairsEngine(t, deps)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/pairs/rwa", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}
