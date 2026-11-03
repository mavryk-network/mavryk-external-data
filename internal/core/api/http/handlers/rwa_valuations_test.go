package handlers

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/storage/repositories"
	"testing"
	"time"
)

type valuationStoreFixture struct {
	rows    []valuation.Snapshot
	buckets []repositories.ValuationBucket
	after   string
}

func (s *valuationStoreFixture) ForContract(_ context.Context, _, contract string) ([]valuation.Snapshot, error) {
	out := []valuation.Snapshot{}
	for _, row := range s.rows {
		if row.Identity.Contract == contract {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *valuationStoreFixture) BySymbol(_ context.Context, _, symbol string) ([]valuation.Snapshot, error) {
	out := []valuation.Snapshot{}
	for _, v := range s.rows {
		for _, p := range v.Valuations {
			if p.Symbol == symbol {
				out = append(out, v)
				break
			}
		}
	}
	return out, nil
}
func (s *valuationStoreFixture) List(_ context.Context, _, after string, limit int) ([]valuation.Snapshot, error) {
	s.after = after
	out := []valuation.Snapshot{}
	for _, v := range s.rows {
		if v.Identity.Contract+":"+v.Identity.TokenID > after {
			out = append(out, v)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (*valuationStoreFixture) Attempts(context.Context, string) ([]repositories.ValuationAttempt, error) {
	return []repositories.ValuationAttempt{{Contract: "KT1unknown", Reason: "identity_ambiguous", AttemptedAt: time.Date(2026, 11, 3, 10, 57, 0, 0, time.UTC)}}, nil
}
func (s *valuationStoreFixture) Series(_ context.Context, _ valuation.SupplyIdentity, _ string, _ app.Interval, _, _ time.Time, _ int) ([]repositories.ValuationBucket, repositories.ValuationCoverage, error) {
	c := repositories.ValuationCoverage{}
	if len(s.buckets) > 0 {
		c.AvailableFrom = &s.buckets[0].Point.AsOf
		c.Through = &s.buckets[len(s.buckets)-1].Point.AsOf
	}
	return s.buckets, c, nil
}

func valuationHTTPFixture() valuation.Snapshot {
	at := time.Date(2026, 11, 3, 10, 57, 0, 0, time.UTC)
	id := valuation.SupplyIdentity{Network: "basenet", Contract: "KT1HR56nVuVDnidsabd2TBB9YvAtKbCbHKpP", TokenID: "0"}
	scale := 6
	price := decimal.NewFromInt(100)
	policy := valuation.Policy{}.Defaulted()
	q := policy.Quality(at, at, "")
	supply := valuation.Supply{Identity: id, Block: valuation.SupplyBlock{Level: 1000, Hash: "Bfixture", Timestamp: at}, IndexedHead: valuation.SupplyBlock{Level: 1002, Hash: "Bhead", Timestamp: at}, ObservedAt: at, Total: "50000000000", Excluded: "10000000000", Circulating: "40000000000", Metadata: valuation.Metadata{Decimals: &scale, Version: "metadata-v1", Source: "mvkt_inline"}, TotalQuality: q, CirculatingQuality: q, ScopeVersion: "scope-v1", ScopeFetchedAt: at, ScopeStatus: "verified", Source: "mvkt", Provenance: "observed"}
	p := valuation.Calculate(supply, valuation.Point{Symbol: "test-usdt", Quote: valuation.Quote{Contract: "KT1Pn5Zpx1bJx5H51btk92pfwvUMCKtp2Q2v", TokenID: "0", Symbol: "usdt"}, PairID: 42, AsOf: at, PriceAt: at, PriceBasis: "last", Price: &price}, policy)
	eur, fdv := decimal.NewFromInt(3680000), decimal.NewFromInt(4600000)
	rate, usdRate, fxAt := decimal.RequireFromString("0.92"), decimal.NewFromInt(1), at.Add(-time.Minute)
	p.Conversions = map[string]valuation.Conversion{"eur": {MarketCap: &eur, FDV: &fdv, Rate: &rate, FXAt: &fxAt, MarketCapQuality: q, FDVQuality: q}, "usd": {MarketCap: p.MarketCap, FDV: p.FDV, Rate: &usdRate, FXAt: &at, MarketCapQuality: q, FDVQuality: q}}
	return valuation.Snapshot{SchemaVersion: 1, PolicyVersion: valuation.PolicyVersion, Revision: "9007199254740993", Identity: id, Supply: supply, AsOf: at, Valuations: []valuation.Point{p}}
}

func valuationEngine(t *testing.T, s *valuationStoreFixture) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	d := RWAValuationDeps{Store: s, Network: "basenet", MaxLimit: 1000, Now: func() time.Time { return valuationHTTPFixture().AsOf }, Price: RWAPriceDeps{Lookup: &stubChartLookup{pair: samplePair()}, Converter: &stubConverter{}, MaxInCurrencies: 5}}
	r.GET("/v1/rwa/:symbol/valuation/latest", d.Latest())
	r.GET("/v1/rwa/valuations/latest", d.Batch())
	r.GET("/v1/rwa/:symbol/series", (RWAChartDeps{Valuation: &d}).Series())
	return r
}
func requestValuation(t *testing.T, r *gin.Engine, path string, status int) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, status, w.Code, w.Body.String())
	return w.Body.Bytes()
}
func valuationGolden(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", "rwa_valuation", name+".json")
	if os.Getenv("UPDATE_VALUATION_FIXTURES") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0750))
		var v any
		require.NoError(t, json.Unmarshal(b, &v))
		formatted, err := json.MarshalIndent(v, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, append(formatted, '\n'), 0600))
	}
	expected, err := os.ReadFile(path) //nolint:gosec // path is a fixed test fixture name, not request input.
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(b))
}

func TestValuationHTTPContracts(t *testing.T) {
	snap := valuationHTTPFixture()
	s := &valuationStoreFixture{rows: []valuation.Snapshot{snap}, buckets: []repositories.ValuationBucket{{T: snap.AsOf.Truncate(time.Hour), Point: snap.Valuations[0]}}}
	r := valuationEngine(t, s)
	latest := requestValuation(t, r, "/v1/rwa/test-usdt/valuation/latest?in=usd,eur", 200)
	valuationGolden(t, "latest", latest)
	valuationGolden(t, "batch", requestValuation(t, r, "/v1/rwa/valuations/latest?in=usd,eur", 200))
	path := "/v1/rwa/test-usdt/series?metric=market_cap&interval=1h&in=eur&from=2026-11-03T10:00:00Z&to=2026-11-03T11:00:00Z"
	chart := requestValuation(t, r, path, 200)
	valuationGolden(t, "series", chart)
	var body map[string]any
	require.NoError(t, json.Unmarshal(chart, &body))
	require.NotContains(t, body, "currency")
	point := body["points"].([]any)[0].(map[string]any)
	require.Equal(t, float64(4000000), point["p"])
	require.Equal(t, float64(3680000), point["eur"])
	require.NotContains(t, point, "usd")
	require.NotContains(t, point, "value")
	require.JSONEq(t, string(latest), string(requestValuation(t, r, "/v1/rwa/test-usdt/valuation/latest?in=usd,eur", 200)))
	for _, query := range []string{"metric=unknown", "metric=market_cap&interval=raw", "metric=market_cap&in=eur,usd", "metric=market_cap&in=unknown", "metric=market_cap&limit=1001"} {
		requestValuation(t, r, "/v1/rwa/test-usdt/series?"+query, 400)
	}
	requestValuation(t, r, "/v1/rwa/valuations/latest?limit=101", 400)
	requestValuation(t, r, "/v1/rwa/valuations/latest?cursor=garbage", 400)
}

func TestValuationHTTPZeroNullAndPagination(t *testing.T) {
	snap := valuationHTTPFixture()
	zero := decimal.Zero
	snap.Valuations[0].MarketCap = &zero
	snap.Valuations[0].Conversions = map[string]valuation.Conversion{}
	s := &valuationStoreFixture{rows: []valuation.Snapshot{snap}, buckets: []repositories.ValuationBucket{{T: snap.AsOf.Truncate(time.Hour), Point: snap.Valuations[0]}}}
	r := valuationEngine(t, s)
	path := "/v1/rwa/test-usdt/series?metric=market_cap&interval=1h&in=eur&from=2026-11-03T10:00:00Z&to=2026-11-03T11:00:00Z"
	raw := requestValuation(t, r, path, 200)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	p := body["points"].([]any)[0].(map[string]any)
	require.Equal(t, float64(0), p["p"])
	require.Contains(t, p, "eur")
	require.Nil(t, p["eur"])
	require.Equal(t, false, body["coverage"].(map[string]any)["complete"])
	s.buckets[0].Invalidated = true
	raw = requestValuation(t, r, path, 200)
	require.NoError(t, json.Unmarshal(raw, &body))
	p = body["points"].([]any)[0].(map[string]any)
	require.Nil(t, p["p"])
	require.Equal(t, "chain_reorg", p["reason"])
	second := snap
	second.Identity.Contract = "KT1zzzz"
	s.rows = append(s.rows, second)
	raw = requestValuation(t, r, "/v1/rwa/valuations/latest?limit=1", 200)
	require.NoError(t, json.Unmarshal(raw, &body))
	cursor := body["next_cursor"].(string)
	raw = requestValuation(t, r, "/v1/rwa/valuations/latest?limit=1&cursor="+cursor, 200)
	require.Equal(t, snap.Identity.Contract+":0", s.after)
	require.NoError(t, json.Unmarshal(raw, &body))
	require.Len(t, body["items"], 1)
	requestValuation(t, r, "/v1/rwa/test-usdt/valuation/latest", 409)
	s.rows = nil
	s.buckets = nil
	raw = requestValuation(t, r, path, 200)
	require.Contains(t, string(raw), `"points":[]`)
	require.Contains(t, string(raw), `"reason":"not_observed"`)
}

func TestValuationHTTPPreservesKnownSupplyWithoutPrice(t *testing.T) {
	s := valuationHTTPFixture()
	s.Valuations = []valuation.Point{}
	s.Reason = "routing_unverified"
	store := &valuationStoreFixture{rows: []valuation.Snapshot{s}}
	pair := samplePair()
	pair.TokenAddr = s.Identity.Contract
	d := RWAValuationDeps{Store: store, Network: s.Identity.Network, Now: func() time.Time { return s.AsOf }, Price: RWAPriceDeps{Lookup: &stubChartLookup{pair: pair}}}
	r := gin.New()
	r.GET("/v1/rwa/:symbol/valuation/latest", d.Latest())
	raw := requestValuation(t, r, "/v1/rwa/test-usdt/valuation/latest", 200)
	var out valuation.Snapshot
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, s.Identity, out.Identity)
	require.Equal(t, "50000000000", out.Supply.Total)
	require.Empty(t, out.Valuations)
	require.Equal(t, "routing_unverified", out.Reason)
}
