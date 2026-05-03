package prices

import (
	"context"
	"errors"
	"testing"
	"time"

	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// fakeCandleRepo is a CandleRepository test double. Captures the last query
// for assertions; returns canned candles or err.
type fakeCandleRepo struct {
	candles []Candle
	err     error
	seen    CandleQuery
}

func (f *fakeCandleRepo) QueryCandles(_ context.Context, q CandleQuery) ([]Candle, error) {
	f.seen = q
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Candle, len(f.candles))
	copy(out, f.candles)
	return out, nil
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// --- ParseInterval ---

func TestParseInterval(t *testing.T) {
	cases := []struct {
		in   string
		want Interval
		ok   bool
	}{
		{"1m", Interval1m, true},
		{"1H", Interval1h, true},
		{" 1d ", Interval1d, true},
		{"raw", IntervalRaw, true},
		{"", "", false},
		{"2m", "", false},
		{"1minute", "", false},
	}
	for _, c := range cases {
		got, ok := ParseInterval(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseInterval(%q) = (%q, %v), want (%q, %v)",
				c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestInterval_IsBucket(t *testing.T) {
	bucket := []Interval{Interval1m, Interval5m, Interval15m, Interval1h, Interval4h, Interval1d}
	for _, iv := range bucket {
		if !iv.IsBucket() {
			t.Errorf("%q.IsBucket() = false, want true", iv)
		}
	}
	notBucket := []Interval{IntervalRaw, ""}
	for _, iv := range notBucket {
		if iv.IsBucket() {
			t.Errorf("%q.IsBucket() = true, want false", iv)
		}
	}
}

// --- Cap validation ---

func TestValidateRange(t *testing.T) {
	caps := DefaultCaps()
	now := time.Now().UTC()
	day := 24 * time.Hour

	cases := []struct {
		name      string
		interval  Interval
		from, to  time.Time
		wantError bool
	}{
		{"latest mode (both zero)", Interval1h, time.Time{}, time.Time{}, false},
		{"to before from", Interval1h, now, now.Add(-time.Hour), true},
		{"1m at the cap (7d)", Interval1m, now.Add(-7 * day), now, false},
		{"1m just over the cap (7d+1s)", Interval1m, now.Add(-(7*day + time.Second)), now, true},
		{"5m within cap (29d)", Interval5m, now.Add(-29 * day), now, false},
		{"5m over cap (31d)", Interval5m, now.Add(-31 * day), now, true},
		{"15m within cap (89d)", Interval15m, now.Add(-89 * day), now, false},
		{"15m over cap (91d)", Interval15m, now.Add(-91 * day), now, true},
		{"1h within cap (364d)", Interval1h, now.Add(-364 * day), now, false},
		{"1h over cap (366d)", Interval1h, now.Add(-366 * day), now, true},
		{"4h within cap (3y)", Interval4h, now.Add(-3 * 365 * day), now, false},
		{"4h over cap (5y)", Interval4h, now.Add(-5 * 365 * day), now, true},
		{"1d unlimited (10y)", Interval1d, now.Add(-10 * 365 * day), now, false},
		{"raw within 7d", IntervalRaw, now.Add(-6 * day), now, false},
		{"raw over 7d", IntervalRaw, now.Add(-9 * day), now, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateRange(caps, c.interval, c.from, c.to)
			if (err != nil) != c.wantError {
				t.Errorf("err=%v, wantError=%v", err, c.wantError)
			}
		})
	}
}

// --- Series projection: Series.Points = Candles.map(close) ---

func TestChartService_Series_ProjectsClosePreservingOrder(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	candles := []Candle{
		{Bucket: bk, Open: dec("1.0"), High: dec("2.0"), Low: dec("0.5"), Close: dec("1.5"), Samples: 12},
		{Bucket: bk.Add(time.Hour), Open: dec("1.5"), High: dec("3.0"), Low: dec("1.0"), Close: dec("2.5"), Samples: 30},
		{Bucket: bk.Add(2 * time.Hour), Open: dec("2.5"), High: dec("2.7"), Low: dec("2.1"), Close: dec("2.2"), Samples: 8},
	}
	repo := &fakeCandleRepo{candles: candles}
	svc := &ChartService{Repo: repo, Caps: DefaultCaps()}

	got, err := svc.Series(context.Background(), CandleQuery{
		EntityKey: "1",
		AuxKey:    "last",
		Interval:  Interval1h,
		From:      bk,
		To:        bk.Add(3 * time.Hour),
	}, nil)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if got.Interval != Interval1h {
		t.Errorf("Interval = %q, want 1h", got.Interval)
	}
	if len(got.Points) != len(candles) {
		t.Fatalf("len(Points) = %d, want %d", len(got.Points), len(candles))
	}
	for i, c := range candles {
		if !got.Points[i].T.Equal(c.Bucket) {
			t.Errorf("Points[%d].T = %s, want %s", i, got.Points[i].T, c.Bucket)
		}
		if !got.Points[i].P.Equal(c.Close) {
			t.Errorf("Points[%d].P = %s, want %s (close)", i, got.Points[i].P, c.Close)
		}
	}
	if repo.seen.Interval != Interval1h {
		t.Errorf("repo.seen.Interval = %q, want 1h", repo.seen.Interval)
	}
}

func TestChartService_Series_EmptyRepoReturnsEmptyPoints(t *testing.T) {
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps()}
	got, err := svc.Series(context.Background(), CandleQuery{Interval: Interval1h}, nil)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(got.Points) != 0 {
		t.Errorf("len(Points) = %d, want 0", len(got.Points))
	}
}

// --- OHLC ---

func TestChartService_OHLC_PassesThroughCandles(t *testing.T) {
	candles := []Candle{
		{Bucket: time.Now().UTC(), Open: dec("1"), High: dec("2"), Low: dec("0.5"), Close: dec("1.5"), Samples: 1},
	}
	repo := &fakeCandleRepo{candles: candles}
	svc := &ChartService{Repo: repo, Caps: DefaultCaps()}

	got, err := svc.OHLC(context.Background(), CandleQuery{Interval: Interval1h}, nil)
	if err != nil {
		t.Fatalf("OHLC: %v", err)
	}
	if len(got.Candles) != 1 || !got.Candles[0].Close.Equal(dec("1.5")) {
		t.Errorf("got %+v", got)
	}
}

// --- Preflight: rejection rules ---

func TestChartService_RejectsRawForCandleEndpoints(t *testing.T) {
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps()}

	if _, err := svc.Series(context.Background(), CandleQuery{Interval: IntervalRaw}, nil); err == nil {
		t.Errorf("Series(raw): want error, got nil")
	}
	if _, err := svc.OHLC(context.Background(), CandleQuery{Interval: IntervalRaw}, nil); err == nil {
		t.Errorf("OHLC(raw): want error, got nil")
	}
}

func TestChartService_RejectsUnknownInterval(t *testing.T) {
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps()}
	_, err := svc.OHLC(context.Background(), CandleQuery{Interval: Interval("3m")}, nil)
	if err == nil {
		t.Errorf("OHLC(3m): want error, got nil")
	}
}

func TestChartService_RejectsLimitOverflow(t *testing.T) {
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps(), MaxLimit: 1000}
	_, err := svc.OHLC(context.Background(), CandleQuery{Interval: Interval1h, Limit: 1001}, nil)
	if err == nil {
		t.Errorf("limit=1001 with MaxLimit=1000: want error, got nil")
	}
}

func TestChartService_FXIn_RequiresConverter(t *testing.T) {
	// With Converter=nil — rejected by the "not enabled" branch.
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps()}
	_, err := svc.Series(context.Background(),
		CandleQuery{Interval: Interval1h, SourceToken: "usdt"},
		[]prices.Currency{prices.CurrencyUSD})
	if err == nil {
		t.Errorf("?in=usd with no converter: want error, got nil")
	}
}

func TestChartService_FXIn_RequiresSourceToken(t *testing.T) {
	// Converter set, but caller forgot SourceToken — preflight catches.
	svc := &ChartService{
		Repo:      &fakeCandleRepo{},
		Caps:      DefaultCaps(),
		Converter: identityConverter{},
	}
	_, err := svc.Series(context.Background(),
		CandleQuery{Interval: Interval1h /* SourceToken: "" */},
		[]prices.Currency{prices.CurrencyUSD})
	if err == nil {
		t.Errorf("?in=usd with no SourceToken: want error, got nil")
	}
}

func TestChartService_FXIn_OHLC_AppliesCloseOfBucketRate(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &fakeCandleRepo{candles: []Candle{
		{Bucket: bk, Open: dec("100"), High: dec("110"), Low: dec("90"), Close: dec("105"), Samples: 10},
		{Bucket: bk.Add(time.Hour), Open: dec("105"), High: dec("115"), Low: dec("100"), Close: dec("110"), Samples: 10},
	}}
	conv := &recordingConverter{rate: dec("1.05")}
	svc := &ChartService{Repo: repo, Caps: DefaultCaps(), Converter: conv, Kind: "rwa"}

	got, err := svc.OHLC(context.Background(),
		CandleQuery{Interval: Interval1h, From: bk, To: bk.Add(2 * time.Hour), SourceToken: "usdt"},
		[]prices.Currency{prices.CurrencyUSD})
	if err != nil {
		t.Fatalf("OHLC: %v", err)
	}
	if len(got.Candles) != 2 {
		t.Fatalf("len = %d", len(got.Candles))
	}
	for i, c := range got.Candles {
		conv0, ok := c.Conv[prices.CurrencyUSD]
		if !ok {
			t.Errorf("candles[%d].Conv missing usd", i)
			continue
		}
		// All four fields scaled by 1.05 — same rate per candle.
		if !conv0.Open.Equal(c.Open.Mul(dec("1.05"))) ||
			!conv0.High.Equal(c.High.Mul(dec("1.05"))) ||
			!conv0.Low.Equal(c.Low.Mul(dec("1.05"))) ||
			!conv0.Close.Equal(c.Close.Mul(dec("1.05"))) {
			t.Errorf("candles[%d] FX mismatch: conv=%+v", i, conv0)
		}
		if !conv0.Rate.Equal(dec("1.05")) {
			t.Errorf("candles[%d] rate = %s, want 1.05", i, conv0.Rate)
		}
	}
	// Per-bucket FX cache: one Convert call per bucketEnd, regardless of
	// candles[i].Conv re-reads. Two distinct buckets → two calls.
	if conv.calls != 2 {
		t.Errorf("converter calls = %d, want 2 (one per bucket)", conv.calls)
	}
}

func TestChartService_FXIn_Series_ProjectsOnlyClose(t *testing.T) {
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &fakeCandleRepo{candles: []Candle{
		{Bucket: bk, Open: dec("100"), High: dec("110"), Low: dec("90"), Close: dec("105"), Samples: 10},
	}}
	conv := &recordingConverter{rate: dec("2.00")}
	svc := &ChartService{Repo: repo, Caps: DefaultCaps(), Converter: conv, Kind: "rwa"}

	got, err := svc.Series(context.Background(),
		CandleQuery{Interval: Interval1h, From: bk, To: bk.Add(time.Hour), SourceToken: "usdt"},
		[]prices.Currency{prices.CurrencyUSD})
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(got.Points) != 1 {
		t.Fatalf("points = %d", len(got.Points))
	}
	convPrice, ok := got.Points[0].Conv[prices.CurrencyUSD]
	if !ok {
		t.Fatalf("Points[0].Conv missing usd")
	}
	// Close * rate = 105 * 2.00 = 210.
	if !convPrice.Equal(dec("210")) {
		t.Errorf("Conv[usd] = %s, want 210", convPrice)
	}
}

func TestChartService_FXIn_OHLC_ConvertFailureDropsTarget(t *testing.T) {
	// One target succeeds, another errors — error case drops the key
	// silently (legacy /v1/rwa/{symbol} ?in= semantics: per-target
	// failures don't fail the request).
	bk := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	repo := &fakeCandleRepo{candles: []Candle{
		{Bucket: bk, Open: dec("100"), High: dec("110"), Low: dec("90"), Close: dec("105")},
	}}
	conv := &perTargetConverter{
		results: map[prices.Currency]ConversionResult{
			prices.CurrencyUSD: {Rate: dec("1.05")},
		},
		errors: map[prices.Currency]error{
			prices.CurrencyEUR: ErrNoFXRate,
		},
	}
	svc := &ChartService{Repo: repo, Caps: DefaultCaps(), Converter: conv, Kind: "rwa"}
	got, err := svc.OHLC(context.Background(),
		CandleQuery{Interval: Interval1h, From: bk, To: bk.Add(time.Hour), SourceToken: "usdt"},
		[]prices.Currency{prices.CurrencyUSD, prices.CurrencyEUR})
	if err != nil {
		t.Fatalf("OHLC: %v", err)
	}
	if _, ok := got.Candles[0].Conv[prices.CurrencyUSD]; !ok {
		t.Errorf("usd conversion should be present")
	}
	if _, ok := got.Candles[0].Conv[prices.CurrencyEUR]; ok {
		t.Errorf("eur conversion should be dropped on error")
	}
}

// --- Cap-overflow now returns 416, not 400 ---

func TestValidateRange_OverCap_Returns416(t *testing.T) {
	caps := DefaultCaps()
	now := time.Now().UTC()
	err := ValidateRange(caps, Interval1m, now.Add(-30*24*time.Hour), now)
	if err == nil {
		t.Fatal("want error for 30d on 1m, got nil")
	}
	var ce *coreerrors.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err is not *coreerrors.Error: %T", err)
	}
	if ce.Code != coreerrors.CodeRangeNotSatisfiable {
		t.Errorf("code = %q, want %q", ce.Code, coreerrors.CodeRangeNotSatisfiable)
	}
}

func TestValidateRange_ToBeforeFrom_Returns400(t *testing.T) {
	caps := DefaultCaps()
	now := time.Now().UTC()
	err := ValidateRange(caps, Interval1h, now, now.Add(-time.Hour))
	if err == nil {
		t.Fatal("want error for to<from")
	}
	var ce *coreerrors.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err is not *coreerrors.Error: %T", err)
	}
	if ce.Code != coreerrors.CodeInvalidArgument {
		t.Errorf("code = %q, want INVALID_ARGUMENT (malformed window != cap overflow)", ce.Code)
	}
}

// --- OHLCV is the parked TODO ---

func TestChartService_OHLCV_ReturnsNotImplemented(t *testing.T) {
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps()}
	_, err := svc.OHLCV(context.Background(), CandleQuery{Interval: Interval1h}, nil)
	if !errors.Is(err, ErrOHLCVNotImplemented) {
		t.Errorf("OHLCV: got %v, want ErrOHLCVNotImplemented", err)
	}
}

func TestChartService_OHLCV_PreflightRunsBefore501(t *testing.T) {
	// Bad interval → 400 from preflight, not 501. Lets handler tests
	// distinguish "client mistake" from "TODO endpoint".
	svc := &ChartService{Repo: &fakeCandleRepo{}, Caps: DefaultCaps()}
	_, err := svc.OHLCV(context.Background(), CandleQuery{Interval: IntervalRaw}, nil)
	if err == nil || errors.Is(err, ErrOHLCVNotImplemented) {
		t.Errorf("OHLCV(raw): want preflight error, got %v", err)
	}
}

// identityConverter returns Rate=1 for every Convert call. Used by tests
// that need a non-nil converter without exercising the rate logic.
type identityConverter struct{}

func (identityConverter) Convert(
	_ context.Context,
	_ prices.Token,
	_ prices.Currency,
	amount decimal.Decimal,
	_ time.Time,
) (ConversionResult, error) {
	return ConversionResult{Amount: amount, Rate: decimal.NewFromInt(1), Identity: true}, nil
}

// recordingConverter returns a fixed rate and counts the calls — lets
// tests assert the per-bucket FX cache hits exactly once per bucket.
type recordingConverter struct {
	rate  decimal.Decimal
	calls int
}

func (r *recordingConverter) Convert(
	_ context.Context,
	_ prices.Token,
	_ prices.Currency,
	amount decimal.Decimal,
	_ time.Time,
) (ConversionResult, error) {
	r.calls++
	return ConversionResult{Amount: amount.Mul(r.rate), Rate: r.rate}, nil
}

// perTargetConverter routes each target currency to a result or error,
// emulating partial FX-source coverage.
type perTargetConverter struct {
	results map[prices.Currency]ConversionResult
	errors  map[prices.Currency]error
}

func (p *perTargetConverter) Convert(
	_ context.Context,
	_ prices.Token,
	target prices.Currency,
	amount decimal.Decimal,
	_ time.Time,
) (ConversionResult, error) {
	if e, ok := p.errors[target]; ok {
		return ConversionResult{}, e
	}
	r := p.results[target]
	r.Amount = amount.Mul(r.Rate)
	return r, nil
}
