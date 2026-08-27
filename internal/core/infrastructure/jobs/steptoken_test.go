package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"quotes/internal/config"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/coingecko"
	"quotes/internal/metrics"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	cgTestToken  = "mvrk"
	cgTestCoinID = "mavryk-network"
	// Two samples per (coin, vs_currency) response — see fakeCoinGecko.ServeHTTP.
	cgSamplesPerRequest = 2
	// GetTokenConfig floors live_lookback_seconds at 600 for a 60s interval, so
	// every tick below asks upstream for exactly this many seconds.
	cgTestLookbackSeconds = 600
)

// cgTestPrices is the price the fake upstream quotes per vs_currency, written as
// the decimal string the mapper must produce. Values are distinct so a currency
// mix-up in the map→PricePoint conversion cannot pass.
var cgTestPrices = map[prices.Currency]string{
	prices.CurrencyUSD: "1.25",
	prices.CurrencyEUR: "1.1",
	prices.CurrencyBTC: "0.00002",
	prices.CurrencyETH: "0.0004",
	prices.CurrencyGBP: "0.95",
	prices.CurrencyCNY: "9.05",
	prices.CurrencyJPY: "187.5",
	prices.CurrencyKRW: "1650",
	prices.CurrencyRUB: "110.25",
	prices.CurrencyAED: "4.59",
}

type cgRequest struct {
	Path   string
	Query  url.Values
	Header http.Header
}

// fakeCoinGecko impersonates the CoinGecko market_chart/range endpoint over a
// real socket and records every inbound request for outbound-shape assertions.
type fakeCoinGecko struct {
	*httptest.Server

	// statusFor maps a vs_currency to the status to answer with; nil = always
	// 200. Set before the first tick.
	statusFor func(vsCurrency string) int

	mu   sync.Mutex
	reqs []cgRequest
}

func newFakeCoinGecko(t *testing.T) *fakeCoinGecko {
	t.Helper()
	f := &fakeCoinGecko{}
	f.Server = httptest.NewServer(f)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCoinGecko) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cur := q.Get("vs_currency")

	f.mu.Lock()
	f.reqs = append(f.reqs, cgRequest{Path: r.URL.Path, Query: q, Header: r.Header.Clone()})
	statusFor := f.statusFor
	f.mu.Unlock()

	if statusFor != nil {
		if code := statusFor(cur); code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
	}

	raw, ok := cgTestPrices[prices.Currency(cur)]
	if !ok {
		http.Error(w, "fake upstream has no price for vs_currency="+cur, http.StatusNotFound)
		return
	}
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	from, err := strconv.ParseInt(q.Get("from"), 10, 64)
	if err != nil {
		http.Error(w, "bad from", http.StatusBadRequest)
		return
	}
	to, err := strconv.ParseInt(q.Get("to"), 10, 64)
	if err != nil {
		http.Error(w, "bad to", http.StatusBadRequest)
		return
	}

	// Samples derived from the requested window so they always clear the
	// mapper's timestamp bounds check without the test pinning wall-clock time.
	body := coingecko.MarketChartRangeResponse{Prices: [][]float64{
		{float64(from) * 1000, price},
		{float64(from+(to-from)/2) * 1000, price},
	}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeCoinGecko) requests() []cgRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cgRequest(nil), f.reqs...)
}

// stubPriceRepo is an in-memory apiprices.Repository: the tick's write side.
type stubPriceRepo struct {
	mu    sync.Mutex
	saves [][]prices.PricePoint
}

var _ apiprices.Repository = (*stubPriceRepo)(nil)

func (r *stubPriceRepo) Save(_ context.Context, points []prices.PricePoint) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, append([]prices.PricePoint(nil), points...))
	return int64(len(points)), nil
}

func (r *stubPriceRepo) Query(context.Context, prices.Query) ([]prices.PricePoint, error) {
	return nil, nil
}

func (r *stubPriceRepo) saveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.saves)
}

func (r *stubPriceRepo) allPoints() []prices.PricePoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []prices.PricePoint
	for _, batch := range r.saves {
		out = append(out, batch...)
	}
	return out
}

// cgLiveTestConfig pins a deterministic transport: one network attempt per
// logical request (no retry storms to count around) and no circuit breaker, so
// the 5xx subtest cannot leak an open breaker into a later one.
func cgLiveTestConfig(baseURL, apiKey string) *config.Config {
	cfg := &config.Config{}
	cfg.Job.Enabled = true
	cfg.Job.IntervalSeconds = 60
	cfg.API.TimeoutSeconds = 5
	cfg.API.OutboundHTTPRetryMaxAttempts = 1
	cfg.API.OutboundHTTPCircuitBreakerDisabled = true
	cfg.CoinGecko.BaseURL = baseURL
	cfg.CoinGecko.APIKey = apiKey
	return cfg
}

func newCGCollector(cfg *config.Config) *tokenCollector {
	return &tokenCollector{
		info:   prices.TokenInfo{Symbol: prices.Token(cgTestToken), CoinGeckoID: cgTestCoinID, Enabled: true},
		client: coingecko.NewClient(cfg.CoinGecko, &cfg.API, cfg.GetTokenTimeout(cgTestToken), nil),
		cfg:    cfg.GetTokenConfig(cgTestToken),
	}
}

func cgCounterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

func cgFetchErrors() prometheus.Counter {
	return metrics.JobErrorsTotal.WithLabelValues("live", string(prices.SourceCoinGecko), cgTestToken, "fetch")
}

func cgRowsAffected() prometheus.Counter {
	return metrics.JobRowsAffectedTotal.WithLabelValues("live", string(prices.SourceCoinGecko), cgTestToken)
}

// TestCoinGeckoLiveJob_CollectOnce_MapsAllCurrenciesToRepository drives a whole
// live tick end to end: real HTTP client → real socket → fake CoinGecko →
// mapper → Repository.
func TestCoinGeckoLiveJob_CollectOnce_MapsAllCurrenciesToRepository(t *testing.T) {
	fake := newFakeCoinGecko(t)
	cfg := cgLiveTestConfig(fake.URL+"/api/v3", "")
	repo := &stubPriceRepo{}
	job := NewCoinGeckoLiveJob(cfg, repo, nil, nil)

	rowsBefore := cgCounterValue(t, cgRowsAffected())
	job.collectOnce(context.Background(), newCGCollector(cfg))

	if got := repo.saveCount(); got != 1 {
		t.Fatalf("Save calls = %d, want 1", got)
	}
	currencies := prices.AllSupportedCurrencies()
	points := repo.allPoints()
	if want := len(currencies) * cgSamplesPerRequest; len(points) != want {
		t.Fatalf("saved points = %d, want %d", len(points), want)
	}

	reqs := fake.requests()
	if len(reqs) == 0 {
		t.Fatal("fake upstream saw no request")
	}
	from, to := cgWindow(t, reqs[0])

	byCurrency := make(map[string][]prices.PricePoint, len(currencies))
	for _, p := range points {
		if p.Source != prices.SourceCoinGecko {
			t.Errorf("point source = %q, want %q", p.Source, prices.SourceCoinGecko)
		}
		if p.EntityKey != cgTestToken {
			t.Errorf("point entity_key = %q, want %q", p.EntityKey, cgTestToken)
		}
		if p.Timestamp.Before(from) || p.Timestamp.After(to) {
			t.Errorf("point ts %s outside requested window [%s, %s]", p.Timestamp, from, to)
		}
		byCurrency[p.Metric] = append(byCurrency[p.Metric], p)
	}

	for _, cur := range currencies {
		got := byCurrency[string(cur)]
		if len(got) != cgSamplesPerRequest {
			t.Errorf("currency %s: %d points, want %d", cur, len(got), cgSamplesPerRequest)
			continue
		}
		for _, p := range got {
			if p.Price.String() != cgTestPrices[cur] {
				t.Errorf("currency %s: price = %s, want %s", cur, p.Price, cgTestPrices[cur])
			}
		}
	}

	// MapToPricePoints promises (ts, metric) ordering; the upsert relies on it.
	for i := 1; i < len(points); i++ {
		prev, cur := points[i-1], points[i]
		if cur.Timestamp.Before(prev.Timestamp) ||
			(cur.Timestamp.Equal(prev.Timestamp) && cur.Metric < prev.Metric) {
			t.Fatalf("points not sorted by (ts, metric) at index %d: %v/%s after %v/%s",
				i, cur.Timestamp, cur.Metric, prev.Timestamp, prev.Metric)
		}
	}

	if got := cgCounterValue(t, cgRowsAffected()) - rowsBefore; got != float64(len(points)) {
		t.Errorf("job_rows_affected_total delta = %v, want %d", got, len(points))
	}
}

// TestCoinGeckoLiveJob_CollectOnce_OutboundRequestShape pins what actually goes
// out on the wire — path, query window, and the API-key header, which is
// host-dependent: a pro header on the demo host is rejected upstream and a demo
// key then looks dead.
func TestCoinGeckoLiveJob_CollectOnce_OutboundRequestShape(t *testing.T) {
	cases := []struct {
		name       string
		basePath   string
		apiKey     string
		wantHeader string
		deadHeader string
	}{
		{
			name:       "demo host uses demo key header",
			basePath:   "/api/v3",
			apiKey:     "demo-key",
			wantHeader: "x-cg-demo-api-key",
			deadHeader: "x-cg-pro-api-key",
		},
		{
			// setAPIKeyHeader selects on a substring of the configured base URL.
			// httptest only hands out a loopback address, so the pro marker rides
			// in the path — the production branch reads c.baseURL either way.
			name:       "pro host uses pro key header",
			basePath:   "/pro-api.coingecko.com/api/v3",
			apiKey:     "pro-key",
			wantHeader: "x-cg-pro-api-key",
			deadHeader: "x-cg-demo-api-key",
		},
		{
			name:     "no api key sends neither header",
			basePath: "/api/v3",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := newFakeCoinGecko(t)
			cfg := cgLiveTestConfig(fake.URL+c.basePath, c.apiKey)
			job := NewCoinGeckoLiveJob(cfg, &stubPriceRepo{}, nil, nil)

			job.collectOnce(context.Background(), newCGCollector(cfg))

			reqs := fake.requests()
			currencies := prices.AllSupportedCurrencies()
			if len(reqs) != len(currencies) {
				t.Fatalf("outbound requests = %d, want %d (one per vs_currency)", len(reqs), len(currencies))
			}

			wantPath := c.basePath + "/coins/" + cgTestCoinID + "/market_chart/range"
			seen := make(map[string]int, len(currencies))
			for i, r := range reqs {
				if r.Path != wantPath {
					t.Errorf("request %d path = %q, want %q", i, r.Path, wantPath)
				}
				if got := r.Header.Get("User-Agent"); got != "mavryk-external-data/1.0" {
					t.Errorf("request %d User-Agent = %q", i, got)
				}
				if c.wantHeader != "" {
					if got := r.Header.Get(c.wantHeader); got != c.apiKey {
						t.Errorf("request %d %s = %q, want %q", i, c.wantHeader, got, c.apiKey)
					}
					if got := r.Header.Get(c.deadHeader); got != "" {
						t.Errorf("request %d must not send %s (got %q)", i, c.deadHeader, got)
					}
				} else {
					for _, h := range []string{"x-cg-demo-api-key", "x-cg-pro-api-key"} {
						if got := r.Header.Get(h); got != "" {
							t.Errorf("request %d sent %s = %q with no key configured", i, h, got)
						}
					}
				}

				from, to := cgWindow(t, r)
				if got := to.Sub(from); got != cgTestLookbackSeconds*time.Second {
					t.Errorf("request %d window = %v, want %ds", i, got, cgTestLookbackSeconds)
				}
				if d := time.Since(to); d < 0 || d > time.Minute {
					t.Errorf("request %d `to` = %s, want ~now", i, to)
				}
				seen[r.Query.Get("vs_currency")]++
			}

			for _, cur := range currencies {
				if seen[string(cur)] != 1 {
					t.Errorf("vs_currency %s requested %d times, want 1", cur, seen[string(cur)])
				}
				delete(seen, string(cur))
			}
			for extra := range seen {
				t.Errorf("unexpected vs_currency requested: %q", extra)
			}
		})
	}
}

// TestCoinGeckoLiveJob_CollectOnce_UpstreamErrorWritesNothing: a hard upstream
// failure must count an error and leave the repository untouched — never a
// half-window write.
func TestCoinGeckoLiveJob_CollectOnce_UpstreamErrorWritesNothing(t *testing.T) {
	fake := newFakeCoinGecko(t)
	fake.statusFor = func(string) int { return http.StatusInternalServerError }
	cfg := cgLiveTestConfig(fake.URL+"/api/v3", "")
	repo := &stubPriceRepo{}
	job := NewCoinGeckoLiveJob(cfg, repo, nil, nil)

	errsBefore := cgCounterValue(t, cgFetchErrors())
	job.collectOnce(context.Background(), newCGCollector(cfg))

	if got := repo.saveCount(); got != 0 {
		t.Errorf("Save calls = %d, want 0 (no partial write on upstream failure)", got)
	}
	if got := cgCounterValue(t, cgFetchErrors()) - errsBefore; got != 1 {
		t.Errorf("job_errors_total{reason=fetch} delta = %v, want 1", got)
	}
	// How many currencies are attempted before giving up is a client policy
	// detail; that upstream was really contacted is not.
	if len(fake.requests()) == 0 {
		t.Error("fake upstream saw no request")
	}
}

// TestCoinGeckoLiveJob_CollectOnce_OneDeadCurrencyKeepsTheRest bounds the blast
// radius of a single deterministic 4xx currency: the healthy currencies of the
// tick must still be persisted. token_prices doubles as the FX source, so
// dropping the whole tick would also black out every `?in=` conversion.
func TestCoinGeckoLiveJob_CollectOnce_OneDeadCurrencyKeepsTheRest(t *testing.T) {
	const dead = prices.CurrencyEUR

	fake := newFakeCoinGecko(t)
	fake.statusFor = func(cur string) int {
		if cur == string(dead) {
			return http.StatusBadRequest
		}
		return http.StatusOK
	}
	cfg := cgLiveTestConfig(fake.URL+"/api/v3", "")
	repo := &stubPriceRepo{}
	job := NewCoinGeckoLiveJob(cfg, repo, nil, nil)

	errsBefore := cgCounterValue(t, cgFetchErrors())
	job.collectOnce(context.Background(), newCGCollector(cfg))

	if got := cgCounterValue(t, cgFetchErrors()) - errsBefore; got != 1 {
		t.Errorf("job_errors_total{reason=fetch} delta = %v, want 1", got)
	}

	var sawUSD, sawDead bool
	for _, r := range fake.requests() {
		switch r.Query.Get("vs_currency") {
		case string(prices.CurrencyUSD):
			sawUSD = true
		case string(dead):
			sawDead = true
		}
	}
	if !sawUSD || !sawDead {
		t.Fatalf("expected both usd and %s to be requested; usd=%v %s=%v", dead, sawUSD, dead, sawDead)
	}
	if got := repo.saveCount(); got != 1 {
		t.Fatalf("Save calls = %d, want 1 (the healthy currencies must survive)", got)
	}
	points := repo.allPoints()
	if want := (len(prices.AllSupportedCurrencies()) - 1) * cgSamplesPerRequest; len(points) != want {
		t.Errorf("saved %d points, want %d (every currency but %s)", len(points), want, dead)
	}
	for _, p := range points {
		if p.Metric == string(dead) {
			t.Errorf("the failing currency %s must not be persisted", dead)
		}
	}
}

func cgWindow(t *testing.T, r cgRequest) (from, to time.Time) {
	t.Helper()
	f, err := strconv.ParseInt(r.Query.Get("from"), 10, 64)
	if err != nil {
		t.Fatalf("from = %q: %v", r.Query.Get("from"), err)
	}
	to2, err := strconv.ParseInt(r.Query.Get("to"), 10, 64)
	if err != nil {
		t.Fatalf("to = %q: %v", r.Query.Get("to"), err)
	}
	return time.Unix(f, 0).UTC(), time.Unix(to2, 0).UTC()
}
