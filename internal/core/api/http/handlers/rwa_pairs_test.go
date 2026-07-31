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

// TestRWAPairs_Shape — happy path: response is an array of catalog entries
// with the full address block, all string fields lowercase where the contract
// says so, no extra fields.
func TestRWAPairs_Shape(t *testing.T) {
	lister := &stubPairsLister{
		pairs: []prices.RWAPair{
			{
				Source: prices.SourceEquiteez, BaseSymbol: "MARS1", QuoteSymbol: "USDT", Enabled: true,
				TokenAddr: "KT1Mars", QuoteAddr: "KT1Usdt", OrderbookAddr: "KT1Book",
			},
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
	wantKeys := []string{"symbol", "base", "quote", "market", "token_addr", "quote_addr", "orderbook_addr", "source"}
	for i, row := range rows {
		if len(row) != len(wantKeys) {
			t.Errorf("row[%d] has %d keys, want exactly %d: %v", i, len(row), len(wantKeys), row)
		}
		for _, k := range wantKeys {
			if _, ok := row[k]; !ok {
				t.Errorf("row[%d] missing key %q", i, k)
			}
		}
	}
	// Lowercased + composed: BaseSymbol "MARS1" → base "mars1", symbol "mars1-usdt".
	first := rows[0]
	if first["symbol"] != "mars1-usdt" || first["base"] != "mars1" || first["quote"] != "usdt" {
		t.Errorf("first = %v/%v/%v, want mars1-usdt/mars1/usdt", first["symbol"], first["base"], first["quote"])
	}
	if first["market"] != "secondary" {
		t.Errorf("market = %v, want secondary", first["market"])
	}
	if first["token_addr"] != "KT1Mars" || first["quote_addr"] != "KT1Usdt" || first["orderbook_addr"] != "KT1Book" {
		t.Errorf("addresses = %v/%v/%v, want KT1Mars/KT1Usdt/KT1Book",
			first["token_addr"], first["quote_addr"], first["orderbook_addr"])
	}
	if first["source"] != "equiteez" {
		t.Errorf("source = %v, want equiteez", first["source"])
	}
	// A pair without addresses (not re-synced yet) renders null, not "".
	second := rows[1]
	for _, k := range []string{"token_addr", "quote_addr", "orderbook_addr"} {
		if second[k] != nil {
			t.Errorf("second.%s = %v, want null", k, second[k])
		}
	}
}

// TestRWAPairs_OrderPreserved — the wire contract is (source, base, quote)
// ascending. The union with launches means the handler must enforce it itself
// rather than trust the repository's ORDER BY, so the input here is
// deliberately unsorted.
func TestRWAPairs_OrderPreserved(t *testing.T) {
	lister := &stubPairsLister{
		pairs: []prices.RWAPair{
			{Source: prices.SourceEquiteez, BaseSymbol: "tst", QuoteSymbol: "usdt", Enabled: true},
			{Source: prices.SourceEquiteez, BaseSymbol: "mars1", QuoteSymbol: "usdt", Enabled: true},
			{Source: prices.SourceEquiteez, BaseSymbol: "tst", QuoteSymbol: "eur", Enabled: true},
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

func catalogRows(t *testing.T, deps RWAPairsDeps) []map[string]any {
	t.Helper()
	r := newRWAPairsEngine(t, deps)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/pairs/rwa", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	return rows
}

// TestRWAPairs_UnionWithLaunches — a launch-only asset joins the catalog as
// market=primary with its token address and null quote/orderbook addresses
// (a primary sale is not settled through the orderbook escrow), and the merged
// list keeps the (source, base, quote) order.
func TestRWAPairs_UnionWithLaunches(t *testing.T) {
	deps := RWAPairsDeps{
		Lookup: &stubPairsLister{pairs: []prices.RWAPair{
			{
				Source: prices.SourceEquiteez, BaseSymbol: "ntbm", QuoteSymbol: "usdt", Enabled: true,
				TokenAddr: "KT1Ntbm", QuoteAddr: "KT1Usdt", OrderbookAddr: "KT1Book",
			},
		}},
		Launches: &stubLaunchLister{launches: []prices.RWALaunch{khbeLaunch()}},
		Source:   prices.SourceEquiteez,
	}
	rows := catalogRows(t, deps)
	if len(rows) != 2 {
		t.Fatalf("len = %d, want 2 (pair + launch)", len(rows))
	}
	// khbe sorts before ntbm.
	launch := rows[0]
	if launch["symbol"] != "khbe-usdt" || launch["market"] != "primary" {
		t.Errorf("launch row = %v/%v, want khbe-usdt/primary", launch["symbol"], launch["market"])
	}
	if launch["token_addr"] != "KT1KHBE" {
		t.Errorf("launch token_addr = %v, want KT1KHBE", launch["token_addr"])
	}
	if launch["quote_addr"] != nil || launch["orderbook_addr"] != nil {
		t.Errorf("launch quote/orderbook addr = %v/%v, want null/null",
			launch["quote_addr"], launch["orderbook_addr"])
	}
	pair := rows[1]
	if pair["symbol"] != "ntbm-usdt" || pair["market"] != "secondary" {
		t.Errorf("pair row = %v/%v, want ntbm-usdt/secondary", pair["symbol"], pair["market"])
	}
}

// TestRWAPairs_UnionDedupesBothFacets — an asset that trades AND is in
// issuance appears ONCE as secondary (mirrors GET /v1/rwa): a duplicate row
// per market would break clients that diff the catalog by symbol.
func TestRWAPairs_UnionDedupesBothFacets(t *testing.T) {
	deps := RWAPairsDeps{
		Lookup: &stubPairsLister{pairs: []prices.RWAPair{
			{
				Source: prices.SourceEquiteez, BaseSymbol: "khbe", QuoteSymbol: "usdt", Enabled: true,
				TokenAddr: "KT1KHBE", QuoteAddr: "KT1Usdt", OrderbookAddr: "KT1Book",
			},
		}},
		Launches: &stubLaunchLister{launches: []prices.RWALaunch{khbeLaunch()}},
		Source:   prices.SourceEquiteez,
	}
	rows := catalogRows(t, deps)
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1 (one asset, two facets)", len(rows))
	}
	if rows[0]["market"] != "secondary" {
		t.Errorf("market = %v, want secondary (pair carries the tx addresses)", rows[0]["market"])
	}
	if rows[0]["orderbook_addr"] != "KT1Book" {
		t.Errorf("orderbook_addr = %v, want KT1Book", rows[0]["orderbook_addr"])
	}
}

// TestRWAPairs_LaunchListerErrorDegrades — the traded catalog must survive a
// launch-catalog failure; clients just see no primary rows this round.
func TestRWAPairs_LaunchListerErrorDegrades(t *testing.T) {
	deps := RWAPairsDeps{
		Lookup: &stubPairsLister{pairs: []prices.RWAPair{
			{Source: prices.SourceEquiteez, BaseSymbol: "mars1", QuoteSymbol: "usdt", Enabled: true},
		}},
		Launches: &stubLaunchLister{err: errors.New("indexer down")},
		Source:   prices.SourceEquiteez,
	}
	rows := catalogRows(t, deps)
	if len(rows) != 1 || rows[0]["symbol"] != "mars1-usdt" {
		t.Fatalf("rows = %v, want the single pair row", rows)
	}
}
