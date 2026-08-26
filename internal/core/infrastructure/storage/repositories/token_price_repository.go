package repositories

import (
	"context"
	"fmt"
	"strings"
	"time"

	apiprices "quotes/internal/core/application/prices"
	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultBatchSize is the upper bound on rows per CreateInBatches call when no
// per-config override is set. Tuned for FT-quotes: 8 currencies × ~24 timestamps
// in a 1d backfill chunk ≈ 200, well under any practical libpq parameter limit.
const DefaultBatchSize = 500

// TokenPriceRepository serves long-format FT prices.
type TokenPriceRepository struct {
	db        *gorm.DB
	batchSize int
}

func NewTokenPriceRepository(db *gorm.DB) *TokenPriceRepository {
	return &TokenPriceRepository{db: db, batchSize: DefaultBatchSize}
}

// WithBatchSize sets the CreateInBatches chunk size. Returned for chainability;
// the receiver is mutated in place. n <= 0 falls back to DefaultBatchSize.
func (r *TokenPriceRepository) WithBatchSize(n int) *TokenPriceRepository {
	if n > 0 {
		r.batchSize = n
	}
	return r
}

func tokenPriceUpsert() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "token_symbol"},
			{Name: "source_code"},
			{Name: "quote_currency"},
			{Name: "ts"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"price"}),
	}
}

// Save inserts a batch of points for one (token, source) into token_prices.
// Conflicts on (token, source, currency, ts) update the price (idempotent re-runs).
// Returns total rows affected.
func (r *TokenPriceRepository) Save(ctx context.Context, points []prices.PricePoint) (int64, error) {
	if len(points) == 0 {
		return 0, nil
	}
	rows := make([]entities.TokenPriceEntity, 0, len(points))
	for _, p := range points {
		rows = append(rows, entities.TokenPriceEntity{
			TokenSymbol:   p.EntityKey,
			SourceCode:    string(p.Source),
			QuoteCurrency: p.Metric,
			Timestamp:     p.Timestamp.UTC(),
			Price:         p.Price,
		})
	}
	res := r.db.WithContext(ctx).Clauses(tokenPriceUpsert()).CreateInBatches(rows, r.batchSize)
	if res.Error != nil {
		return 0, fmt.Errorf("save token_prices batch: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// Query returns the price points matching q. Result ordering: ts ASC, metric ASC.
// IsLatest queries return the freshest rows; semantics differ from a time window
// query because we cannot put DISTINCT ON together with arbitrary metric filtering
// in one shot, so we fan out (see LatestPerMetric below).
func (r *TokenPriceRepository) Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	if q.IsLatest() {
		return r.latestPerMetric(ctx, q)
	}

	tx := r.db.WithContext(ctx).Model(&entities.TokenPriceEntity{}).
		Where("token_symbol = ? AND source_code = ?", q.EntityKey, string(q.Source))
	if !q.From.IsZero() {
		tx = tx.Where("ts >= ?", q.From.UTC())
	}
	if !q.To.IsZero() {
		tx = tx.Where("ts <= ?", q.To.UTC())
	}
	if q.HasMetricFilter() {
		tx = tx.Where("quote_currency IN ?", q.Metrics)
	}
	tx = tx.Order("ts ASC").Order("quote_currency ASC")
	if q.Limit > 0 {
		tx = tx.Limit(q.Limit)
	}
	var rows []entities.TokenPriceEntity
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query token_prices: %w", err)
	}
	return tokenEntitiesToPoints(rows, q.Source), nil
}

// latestPerMetric returns the freshest row per (currency) for one (token, source).
// Postgres-friendly DISTINCT ON. Limit is applied AFTER deduplication.
func (r *TokenPriceRepository) latestPerMetric(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	var rows []entities.TokenPriceEntity
	tx := r.db.WithContext(ctx).
		Raw(`SELECT DISTINCT ON (quote_currency)
		            token_symbol, source_code, quote_currency, ts, price
		     FROM token_prices
		     WHERE token_symbol = ? AND source_code = ?`+metricFilterFragment(q.Metrics)+`
		     ORDER BY quote_currency, ts DESC`,
			rawArgs(q)...)
	if err := tx.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query latest token_prices: %w", err)
	}
	out := tokenEntitiesToPoints(rows, q.Source)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// LatestRateAtOrBefore returns the freshest token_prices row for the
// (token, source, currency) tuple whose `ts` is at or before `at`.
// Returns (PricePoint{}, false, nil) when no such row exists — typically
// because the request asks for a timestamp before the backfill window.
//
// Implementation: a single-row index seek on the PK
// `(token_symbol, source_code, quote_currency, ts)`. O(log n), no full
// scan. Used by the FX converter (Decision #19 in the price-change
// design doc / `fix_todo.md`) to honour the `ts` argument it has
// always accepted but was previously ignoring.
func (r *TokenPriceRepository) LatestRateAtOrBefore(
	ctx context.Context,
	source prices.Source,
	tokenSymbol string,
	quoteCurrency string,
	at time.Time,
) (prices.PricePoint, bool, error) {
	var rows []entities.TokenPriceEntity
	err := r.db.WithContext(ctx).Raw(
		`SELECT token_symbol, source_code, quote_currency, ts, price
		   FROM token_prices
		  WHERE token_symbol = ? AND source_code = ? AND quote_currency = ?
		    AND ts <= ?
		  ORDER BY ts DESC
		  LIMIT 1`,
		tokenSymbol, string(source), quoteCurrency, at.UTC(),
	).Scan(&rows).Error
	if err != nil {
		return prices.PricePoint{}, false, fmt.Errorf("latest rate at-or-before: %w", err)
	}
	if len(rows) == 0 {
		return prices.PricePoint{}, false, nil
	}
	p := tokenEntitiesToPoints(rows[:1], source)[0]
	return p, true, nil
}

// Count returns the total row count for (token, source) — used by /count endpoint.
func (r *TokenPriceRepository) Count(ctx context.Context, source prices.Source, tokenSymbol string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).
		Model(&entities.TokenPriceEntity{}).
		Where("token_symbol = ? AND source_code = ?", tokenSymbol, string(source)).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count token_prices: %w", err)
	}
	return n, nil
}

// OldestTimestamp returns MIN(ts) across all metrics for (token, source).
func (r *TokenPriceRepository) OldestTimestamp(ctx context.Context, source prices.Source, tokenSymbol string) (sentinel, error) {
	var ts *struct {
		MinTs *string
	}
	row := r.db.WithContext(ctx).Raw(
		`SELECT MIN(ts)::text AS min_ts
		   FROM token_prices
		  WHERE token_symbol = ? AND source_code = ?`,
		tokenSymbol, string(source))
	var holder struct {
		MinTs *string
	}
	if err := row.Scan(&holder).Error; err != nil {
		return sentinel{}, fmt.Errorf("oldest token_prices ts: %w", err)
	}
	ts = &holder
	if ts.MinTs == nil || *ts.MinTs == "" {
		return sentinel{Found: false}, nil
	}
	return parseSentinel(*ts.MinTs)
}

// LatestTimestamp returns MAX(ts) across all metrics for (token, source).
func (r *TokenPriceRepository) LatestTimestamp(ctx context.Context, source prices.Source, tokenSymbol string) (sentinel, error) {
	var holder struct {
		MaxTs *string
	}
	row := r.db.WithContext(ctx).Raw(
		`SELECT MAX(ts)::text AS max_ts
		   FROM token_prices
		  WHERE token_symbol = ? AND source_code = ?`,
		tokenSymbol, string(source))
	if err := row.Scan(&holder).Error; err != nil {
		return sentinel{}, fmt.Errorf("latest token_prices ts: %w", err)
	}
	if holder.MaxTs == nil || *holder.MaxTs == "" {
		return sentinel{Found: false}, nil
	}
	return parseSentinel(*holder.MaxTs)
}

// metricFilterFragment renders an optional `AND quote_currency IN (?,?,...)`
// fragment. Each metric becomes its own placeholder so pgx-via-GORM binds
// them as scalars; using `= ANY(?)` with a Go []string here would push the
// whole slice as a single text-array literal and fail with `malformed array
// literal` (pgx does not auto-wrap slices for raw SQL). All values still
// flow through prepared-statement parameters — no SQL injection.
func metricFilterFragment(metrics []string) string {
	if len(metrics) == 0 {
		return ""
	}
	return ` AND quote_currency IN (` + strings.Repeat("?,", len(metrics)-1) + `?)`
}

func rawArgs(q prices.Query) []any {
	args := []any{q.EntityKey, string(q.Source)}
	for _, m := range q.Metrics {
		args = append(args, m)
	}
	return args
}

// QueryCandles serves chart reads for FA tokens. Every supported interval
// resolves to a continuous aggregate; raw `token_prices` is never scanned
// on the hot path.
//
// AuxKey contract: "<source>|<currency>" (e.g. "coingecko|usd"). The source
// component is required because `token_prices_*` aggregates are keyed by
// (token_symbol, source_code, quote_currency).
//
// Source mapping per interval:
//   - 1m / 1h / 1d → direct CA SELECT (token_prices_1m / _1h / _1d).
//   - 5m / 15m     → re-bucket from token_prices_1m at query time.
//   - 4h           → re-bucket from token_prices_1h at query time.
//
// Re-bucket preserves OHLC semantics via TimescaleDB's first()/last() over
// the inner CA's bucket column; max(max_price)/min(min_price) over the
// per-bucket aggregates are exact (associative aggregations).
//
// ChartService.preflight rejects raw before it gets here.
func (r *TokenPriceRepository) QueryCandles(
	ctx context.Context,
	q apiprices.CandleQuery,
) ([]apiprices.Candle, error) {
	source, currency, err := parseTokenCandleAuxKey(q.AuxKey)
	if err != nil {
		return nil, err
	}
	src, ok := tokenCandleSource(q.Interval)
	if !ok {
		return nil, coreerrors.InvalidArgument(
			"Interval '" + string(q.Interval) + "' is not supported for FA charts")
	}

	// view + rebucket spec are from a closed enum — safe to interpolate.
	// Filters and limits remain placeholder-bound.
	where := "token_symbol = ? AND source_code = ? AND quote_currency = ?"
	args := []any{q.EntityKey, source, currency}
	if !q.From.IsZero() {
		where += " AND bucket >= ?"
		args = append(args, q.From.UTC())
	}
	if !q.To.IsZero() {
		where += " AND bucket < ?"
		args = append(args, q.To.UTC())
	}

	sql := buildCandleSQL(src, where)
	if q.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", q.Limit)
	}

	var rows []tokenCandleRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query %s: %w", src.view, err)
	}

	// Rows arrive newest-first (see buildCandleSQL); reverse to ascending.
	out := make([]apiprices.Candle, len(rows))
	for i, rec := range rows {
		out[len(rows)-1-i] = apiprices.Candle{
			Bucket:  rec.Bucket.UTC(),
			Open:    rec.OpenPrice,
			High:    rec.HighPrice,
			Low:     rec.LowPrice,
			Close:   rec.ClosePrice,
			Samples: rec.Samples,
			// VolumeBase / VolumeQuote: nullable, deliberately left zero —
			// FA volume column lands in a follow-up (see ADR-0015).
		}
	}
	return out, nil
}

// Compile-time check: TokenPriceRepository satisfies the chart contract.
var _ apiprices.CandleRepository = (*TokenPriceRepository)(nil)

// tokenCandleSource maps an interval to its backing CA + optional re-bucket
// width. Direct intervals (1m / 1h / 1d) leave Rebucket empty; derived
// intervals (5m / 15m / 4h) carry the time_bucket() spec for the outer
// SELECT (see ADR-0015).
func tokenCandleSource(iv apiprices.Interval) (candleSource, bool) {
	switch iv {
	case apiprices.Interval1m:
		return candleSource{view: "token_prices_1m"}, true
	case apiprices.Interval5m:
		return candleSource{view: "token_prices_1m", rebucket: "5 minutes"}, true
	case apiprices.Interval15m:
		return candleSource{view: "token_prices_1m", rebucket: "15 minutes"}, true
	case apiprices.Interval1h:
		return candleSource{view: "token_prices_1h"}, true
	case apiprices.Interval4h:
		return candleSource{view: "token_prices_1h", rebucket: "4 hours"}, true
	case apiprices.Interval1d:
		return candleSource{view: "token_prices_1d"}, true
	default:
		return candleSource{}, false
	}
}

// parseTokenCandleAuxKey splits "<source>|<currency>" into its parts. Both
// halves must be non-empty; "|" alone or one side missing is a 400 (the
// handler builds this string, so any failure here is a programming error,
// surfaced as an explicit message rather than a silent empty filter).
func parseTokenCandleAuxKey(aux string) (source, currency string, err error) {
	source, currency, ok := strings.Cut(aux, "|")
	if !ok || strings.TrimSpace(source) == "" || strings.TrimSpace(currency) == "" {
		return "", "", coreerrors.InvalidArgument(
			`AuxKey must be "<source>|<currency>" (e.g. "coingecko|usd")`)
	}
	return source, currency, nil
}

// tokenCandleRow is the wire-compat row for the SELECT in QueryCandles. The
// CA columns are numeric in storage and decoded into shopspring/decimal so
// no float64 round-trips happen on the read path.
type tokenCandleRow struct {
	Bucket     time.Time       `gorm:"column:bucket"`
	OpenPrice  decimal.Decimal `gorm:"column:open_price"`
	HighPrice  decimal.Decimal `gorm:"column:high_price"`
	LowPrice   decimal.Decimal `gorm:"column:low_price"`
	ClosePrice decimal.Decimal `gorm:"column:close_price"`
	Samples    int64           `gorm:"column:samples"`
}

func tokenEntitiesToPoints(rows []entities.TokenPriceEntity, fallbackSource prices.Source) []prices.PricePoint {
	out := make([]prices.PricePoint, len(rows))
	for i, e := range rows {
		src := prices.Source(e.SourceCode)
		if src == "" {
			src = fallbackSource
		}
		out[i] = prices.PricePoint{
			Source:    src,
			EntityKey: e.TokenSymbol,
			Timestamp: e.Timestamp,
			Metric:    e.QuoteCurrency,
			Price:     e.Price,
		}
	}
	return out
}
