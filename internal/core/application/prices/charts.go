package prices

import (
	"context"
	"errors"
	"strings"
	"time"

	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/metrics"

	"github.com/shopspring/decimal"
)

// Interval is a chart bucket size — the wire/storage representation. Stored
// continuous-aggregates are named after these (e.g. `rwa_quote_prices_1h`),
// so changing the string form is a breaking change.
type Interval string

const (
	// IntervalRaw is tick-level data. Only valid for /series; rejected by
	// the OHLC and OHLCV paths since "open of a tick" is meaningless.
	IntervalRaw Interval = "raw"
	Interval1m  Interval = "1m"
	Interval5m  Interval = "5m"
	Interval15m Interval = "15m"
	Interval1h  Interval = "1h"
	Interval4h  Interval = "4h"
	Interval1d  Interval = "1d"
)

// AllChartIntervals is the canonical accept-list, ordered finest→coarsest.
// The order matters for fixture-based tests; do not sort alphabetically.
var AllChartIntervals = []Interval{
	IntervalRaw, Interval1m, Interval5m, Interval15m, Interval1h, Interval4h, Interval1d,
}

// ParseInterval normalises s (lower-case, trim) and validates it against
// AllChartIntervals. Empty string returns ok=false — callers decide whether
// "missing" maps to a default or to a 400.
func ParseInterval(s string) (Interval, bool) {
	iv := Interval(strings.ToLower(strings.TrimSpace(s)))
	if iv == "" {
		return "", false
	}
	for _, v := range AllChartIntervals {
		if v == iv {
			return iv, true
		}
	}
	return "", false
}

// IsBucket reports whether the interval represents a candle bucket (i.e.
// everything except raw). Used to gate /ohlc and /ohlcv handlers.
func (i Interval) IsBucket() bool {
	return i != IntervalRaw && i != ""
}

// BucketDuration returns the wall-clock width of one bucket. Used by the
// close-of-bucket FX path (see ADR-0015) to compute `bucket + interval`.
// Returns ok=false for IntervalRaw and unknown values.
func BucketDuration(iv Interval) (time.Duration, bool) {
	switch iv {
	case Interval1m:
		return time.Minute, true
	case Interval5m:
		return 5 * time.Minute, true
	case Interval15m:
		return 15 * time.Minute, true
	case Interval1h:
		return time.Hour, true
	case Interval4h:
		return 4 * time.Hour, true
	case Interval1d:
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

// CandleQuery is the application-layer parameter object for fetching candles.
//
// EntityKey/AuxKey are interpreted by the concrete CandleRepository:
//   - RWA repo: EntityKey = pair_id (string form), AuxKey = side ("last").
//   - FA repo:  EntityKey = token_symbol,           AuxKey = "<source>|<currency>".
//
// SourceToken is the FX-source token used when `in` is supplied — for RWA
// it's the pair's native quote (e.g. "usdt") promoted to a registered Token.
// FA charts don't use FX (currency lives in storage), so SourceToken stays
// zero-value there.
//
// Keeping the contract opaque at the application layer lets ChartService be
// shared across both classes without per-class branching.
type CandleQuery struct {
	EntityKey   string
	AuxKey      string
	From, To    time.Time
	Interval    Interval
	Limit       int
	SourceToken prices.Token
}

// Candle is the application-layer view of one bucket. Volume fields are
// nullable because OHLCV is parked as a future-stage TODO (ADR-0015) — once
// volume ingestion lands they're populated; until then repositories return
// Valid=false.
//
// Conv is populated by ChartService when `in` is non-empty (close-of-bucket
// FX; see ADR-0015 / ADR-0013). Repositories never set it.
type Candle struct {
	Bucket time.Time

	Open  decimal.Decimal
	High  decimal.Decimal
	Low   decimal.Decimal
	Close decimal.Decimal

	VolumeBase  decimal.NullDecimal
	VolumeQuote decimal.NullDecimal

	Samples int64

	Conv map[prices.Currency]ConvertedCandle
}

// ConvertedCandle holds one bucket's OHLC after FX conversion at the
// close-of-bucket rate (see ADR-0015 for the rationale; ADR-0013 for the
// underlying converter contract). `Rate` and `RateTS` make the conversion
// auditable on the wire.
type ConvertedCandle struct {
	Open   decimal.Decimal
	High   decimal.Decimal
	Low    decimal.Decimal
	Close  decimal.Decimal
	Rate   decimal.Decimal
	RateTS time.Time
}

// CandleRepository is the storage contract ChartService relies on. Both
// RWAPriceRepository and TokenPriceRepository satisfy this.
type CandleRepository interface {
	QueryCandles(ctx context.Context, q CandleQuery) ([]Candle, error)
}

// Series is the line/series response — one point per bucket, close only.
type Series struct {
	Interval Interval
	Points   []SeriesPoint
}

// SeriesPoint is one (timestamp, close-price) pair. Conv is populated when
// `in` is non-empty: target-currency-keyed map of the close price after FX.
type SeriesPoint struct {
	T    time.Time
	P    decimal.Decimal
	Conv map[prices.Currency]decimal.Decimal
}

// OHLC is a list of candles without volume.
type OHLC struct {
	Interval Interval
	Candles  []Candle
}

// OHLCV is a list of candles with volume. Returned by ChartService.OHLCV
// only after Stage 4 lifts the TODO; currently the method always returns
// ErrOHLCVNotImplemented.
type OHLCV struct {
	Interval Interval
	Candles  []Candle
}

// ErrOHLCVNotImplemented is the sentinel returned by ChartService.OHLCV
// until volume ingestion ships (see ADR-0015). The HTTP layer maps this to
// 501 with code "OHLCV_NOT_IMPLEMENTED" (see handlers.NotImplementedOHLCV).
var ErrOHLCVNotImplemented = errors.New("OHLCV is not yet implemented")

// DefaultCaps returns the maximum (to-from) window per interval (see ADR-0015).
// Intervals absent from the map are treated as unlimited (currently only 1d).
// Caps are tuned so a single request never returns more than ~10k candles.
func DefaultCaps() map[Interval]time.Duration {
	const day = 24 * time.Hour
	return map[Interval]time.Duration{
		IntervalRaw: 7 * day,
		Interval1m:  7 * day,
		Interval5m:  30 * day,
		Interval15m: 90 * day,
		Interval1h:  365 * day,
		Interval4h:  4 * 365 * day,
		// Interval1d: omitted — unlimited.
	}
}

// ValidateRange enforces the per-interval cap (see ADR-0015).
//
//   - Both From and To zero → latest mode, no range to validate.
//   - To before From         → 400 INVALID_ARGUMENT (malformed window).
//   - (To-From) > caps[iv]   → 416 RANGE_NOT_SATISFIABLE (range overflow).
//   - iv missing from caps   → unlimited (currently only 1d).
func ValidateRange(caps map[Interval]time.Duration, iv Interval, from, to time.Time) error {
	if from.IsZero() && to.IsZero() {
		return nil
	}
	if to.Before(from) {
		return coreerrors.InvalidArgument("Invalid time range: 'from' must be before 'to'")
	}
	cap, ok := caps[iv]
	if !ok {
		return nil
	}
	if to.Sub(from) > cap {
		return coreerrors.RangeNotSatisfiable(
			"Time range exceeds cap for interval '" + string(iv) + "'")
	}
	return nil
}

// ChartService composes CandleRepository, cap validation, and close-of-bucket
// FX conversion. OHLCV is stubbed to ErrOHLCVNotImplemented until Stage 4.
//
// Kind labels Prometheus metrics — typically "fa" or "rwa". Empty kind is
// allowed but rolls all class metrics into one bucket; production wiring
// should always set it.
type ChartService struct {
	Repo      CandleRepository
	Converter PriceConverter // optional; required for `?in=` to work
	Caps      map[Interval]time.Duration
	MaxLimit  int
	Kind      string
}

// Series returns one (t, close) point per bucket. interval=raw is *not* served
// here — the handler routes raw to the existing point repository. Service-level
// callers must pass a bucket interval.
//
// When `in` is non-empty, each point gets a Conv entry per target currency
// using the close-of-bucket rate — same model as OHLC, with only the close
// dimension surfaced (see ADR-0015).
func (s *ChartService) Series(
	ctx context.Context,
	q CandleQuery,
	in []prices.Currency,
) (Series, error) {
	if err := s.preflight(q, in); err != nil {
		return Series{}, err
	}
	defer s.observeDuration(q.Interval, time.Now())

	candles, err := s.Repo.QueryCandles(ctx, q)
	if err != nil {
		return Series{}, err
	}
	if len(in) > 0 && len(candles) > 0 {
		if err := s.applyFXToCandles(ctx, q, candles, in); err != nil {
			return Series{}, err
		}
	}

	points := make([]SeriesPoint, len(candles))
	for i, c := range candles {
		p := SeriesPoint{T: c.Bucket, P: c.Close}
		if len(c.Conv) > 0 {
			p.Conv = make(map[prices.Currency]decimal.Decimal, len(c.Conv))
			for k, v := range c.Conv {
				p.Conv[k] = v.Close
			}
		}
		points[i] = p
	}
	s.observeRows(q.Interval, len(points))
	return Series{Interval: q.Interval, Points: points}, nil
}

// OHLC returns the bucket-level candles without volume. When `in` is
// non-empty, each candle gets a Conv entry per target currency carrying the
// converted o/h/l/c plus rate/rate_ts (see ADR-0015).
func (s *ChartService) OHLC(
	ctx context.Context,
	q CandleQuery,
	in []prices.Currency,
) (OHLC, error) {
	if err := s.preflight(q, in); err != nil {
		return OHLC{}, err
	}
	defer s.observeDuration(q.Interval, time.Now())

	candles, err := s.Repo.QueryCandles(ctx, q)
	if err != nil {
		return OHLC{}, err
	}
	if len(in) > 0 && len(candles) > 0 {
		if err := s.applyFXToCandles(ctx, q, candles, in); err != nil {
			return OHLC{}, err
		}
	}
	s.observeRows(q.Interval, len(candles))
	return OHLC{Interval: q.Interval, Candles: candles}, nil
}

// OHLCV is reserved for the volume-ingestion follow-up (see ADR-0015).
// Currently always returns ErrOHLCVNotImplemented; preflight runs anyway
// so unit tests can still distinguish "bad request → 400" from "endpoint
// TODO → 501".
func (s *ChartService) OHLCV(
	_ context.Context,
	q CandleQuery,
	in []prices.Currency,
) (OHLCV, error) {
	if err := s.preflight(q, in); err != nil {
		return OHLCV{}, err
	}
	return OHLCV{}, ErrOHLCVNotImplemented
}

// preflight runs the validations every chart endpoint shares: bucket-only
// interval, range cap, limit cap, `in` requires a wired Converter.
func (s *ChartService) preflight(q CandleQuery, in []prices.Currency) error {
	if !q.Interval.IsBucket() {
		// raw is series-handler-level (direct PointRepository.Query); rejecting
		// here keeps OHLC/OHLCV honest — there's no such thing as "raw candles".
		return coreerrors.InvalidArgument(
			"Invalid 'interval' parameter: must be one of 1m, 5m, 15m, 1h, 4h, 1d")
	}
	if _, ok := ParseInterval(string(q.Interval)); !ok {
		return coreerrors.InvalidArgument(
			"Invalid 'interval' parameter: " + string(q.Interval))
	}
	if err := ValidateRange(s.Caps, q.Interval, q.From, q.To); err != nil {
		// Cap-hit observability: count the rejection so dashboards can spot
		// clients pulling past the per-interval ceiling.
		s.observeCapHit(q.Interval, err)
		return err
	}
	if s.MaxLimit > 0 && q.Limit > s.MaxLimit {
		s.observeCapHit(q.Interval, capHitLimit{})
		return coreerrors.InvalidArgument("'limit' parameter exceeds maximum")
	}
	if len(in) > 0 {
		if s.Converter == nil {
			return coreerrors.InvalidArgument("'in' parameter is not enabled on this server")
		}
		if q.SourceToken == "" {
			return coreerrors.InvalidArgument(
				"'in' is supplied but no source token is wired for this class of charts")
		}
	}
	return nil
}

// applyFXToCandles fills Conv on each candle in-place using the close-of-bucket
// rate (see ADR-0015 for the chart contract; ADR-0013 for the converter).
// All four price fields share one rate per (bucket, target) so the candle
// stays valid (l ≤ o,c ≤ h).
//
// Per-request FX cache: within one chart query we touch the same
// (bucketEnd, target) at most once. Multiple targets per candle is supported
// but RWA charts cap to ≤1 at the handler — the cache costs us nothing for
// the cap=1 case and protects multi-target callers if we ever lift the cap.
func (s *ChartService) applyFXToCandles(
	ctx context.Context,
	q CandleQuery,
	candles []Candle,
	in []prices.Currency,
) error {
	dur, ok := BucketDuration(q.Interval)
	if !ok {
		// preflight already rejected non-bucket intervals; defensive.
		return coreerrors.Internal("interval has no fixed duration: "+string(q.Interval), nil)
	}

	type fxKey struct {
		bucketEnd time.Time
		target    prices.Currency
	}
	cache := make(map[fxKey]ConversionResult, len(candles))

	for i := range candles {
		bucketEnd := candles[i].Bucket.Add(dur)
		for _, target := range in {
			key := fxKey{bucketEnd: bucketEnd, target: target}
			res, hit := cache[key]
			if !hit {
				r, err := s.Converter.Convert(ctx, q.SourceToken, target, decimal.NewFromInt(1), bucketEnd)
				if err != nil {
					// Per-target failures drop silently — same model as
					// the legacy /v1/rwa/{symbol} `?in=` semantics. The
					// `fx_conversions_total{result=...}` counter inside
					// the Converter records the reason.
					continue
				}
				if r.Stale {
					metrics.FXStaleResponsesTotal.WithLabelValues(string(target)).Inc()
				}
				res = r
				cache[key] = r
			}
			if candles[i].Conv == nil {
				candles[i].Conv = make(map[prices.Currency]ConvertedCandle, len(in))
			}
			candles[i].Conv[target] = ConvertedCandle{
				Open:   candles[i].Open.Mul(res.Rate),
				High:   candles[i].High.Mul(res.Rate),
				Low:    candles[i].Low.Mul(res.Rate),
				Close:  candles[i].Close.Mul(res.Rate),
				Rate:   res.Rate,
				RateTS: res.RateTS,
			}
		}
	}
	return nil
}

// --- metric helpers ---

func (s *ChartService) kindLabel() string {
	if s.Kind == "" {
		return "unknown"
	}
	return s.Kind
}

func (s *ChartService) observeDuration(iv Interval, start time.Time) {
	metrics.ChartQueryDurationSeconds.
		WithLabelValues(s.kindLabel(), string(iv)).
		Observe(time.Since(start).Seconds())
}

func (s *ChartService) observeRows(iv Interval, n int) {
	metrics.ChartQueryRows.
		WithLabelValues(s.kindLabel(), string(iv)).
		Observe(float64(n))
}

// capHitLimit is a typed sentinel for `?limit=` cap; observeCapHit checks
// for it via type-assert to label "limit" instead of "range_exceeded".
type capHitLimit struct{}

func (s *ChartService) observeCapHit(iv Interval, reason any) {
	label := "range_exceeded"
	if _, ok := reason.(capHitLimit); ok {
		label = "limit"
	}
	metrics.ChartQueryCapHitsTotal.
		WithLabelValues(s.kindLabel(), string(iv), label).
		Inc()
}
