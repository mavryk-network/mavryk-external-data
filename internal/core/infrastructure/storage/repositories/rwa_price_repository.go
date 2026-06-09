package repositories

import (
	"context"
	"fmt"
	"strconv"
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

// RWAPriceRepository serves long-format RWA orderbook prices.
type RWAPriceRepository struct {
	db        *gorm.DB
	batchSize int
}

func NewRWAPriceRepository(db *gorm.DB) *RWAPriceRepository {
	return &RWAPriceRepository{db: db, batchSize: DefaultBatchSize}
}

// WithBatchSize lets main.go inject Database.BatchSize. n <= 0 keeps the default.
func (r *RWAPriceRepository) WithBatchSize(n int) *RWAPriceRepository {
	if n > 0 {
		r.batchSize = n
	}
	return r
}

func rwaPriceUpsert() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{
			{Name: "pair_id"},
			{Name: "side"},
			{Name: "ts"},
		},
		DoUpdates: clause.AssignmentColumns([]string{"price", "size"}),
	}
}

// Save persists a batch of RWA points. EntityKey must be the decimal pair_id.
func (r *RWAPriceRepository) Save(ctx context.Context, points []prices.PricePoint) (int64, error) {
	if len(points) == 0 {
		return 0, nil
	}
	rows := make([]entities.RWAQuotePriceEntity, 0, len(points))
	for _, p := range points {
		pid, err := strconv.ParseInt(p.EntityKey, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("rwa save: invalid pair_id %q: %w", p.EntityKey, err)
		}
		rows = append(rows, entities.RWAQuotePriceEntity{
			PairID:    pid,
			Side:      p.Metric,
			Timestamp: p.Timestamp.UTC(),
			Price:     p.Price,
			Size:      p.Size,
		})
	}
	res := r.db.WithContext(ctx).Clauses(rwaPriceUpsert()).CreateInBatches(rows, r.batchSize)
	if res.Error != nil {
		return 0, fmt.Errorf("save rwa_quote_prices batch: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// Query returns RWA price points for (pair_id) under the given filters.
func (r *RWAPriceRepository) Query(ctx context.Context, q prices.Query) ([]prices.PricePoint, error) {
	pid, err := strconv.ParseInt(q.EntityKey, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("rwa query: invalid pair_id %q: %w", q.EntityKey, err)
	}

	if q.IsLatest() {
		return r.latestPerSide(ctx, pid, q)
	}

	tx := r.db.WithContext(ctx).Model(&entities.RWAQuotePriceEntity{}).
		Where("pair_id = ?", pid)
	if !q.From.IsZero() {
		tx = tx.Where("ts >= ?", q.From.UTC())
	}
	if !q.To.IsZero() {
		tx = tx.Where("ts <= ?", q.To.UTC())
	}
	if q.HasMetricFilter() {
		tx = tx.Where("side IN ?", q.Metrics)
	}
	tx = tx.Order("ts ASC").Order("side ASC")
	if q.Limit > 0 {
		tx = tx.Limit(q.Limit)
	}
	var rows []entities.RWAQuotePriceEntity
	if err := tx.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query rwa_quote_prices: %w", err)
	}
	return rwaEntitiesToPoints(rows, q.Source), nil
}

func (r *RWAPriceRepository) latestPerSide(ctx context.Context, pairID int64, q prices.Query) ([]prices.PricePoint, error) {
	// Build a `side IN (?,?,...)` fragment with one placeholder per metric;
	// `= ANY(?)` with a Go []string would push the whole slice as a single
	// text-array literal under pgx/raw-SQL and fail with `malformed array
	// literal`. All values still flow through prepared-statement parameters.
	args := []any{pairID}
	frag := ""
	if q.HasMetricFilter() {
		frag = " AND side IN (" + strings.Repeat("?,", len(q.Metrics)-1) + "?)"
		for _, m := range q.Metrics {
			args = append(args, m)
		}
	}
	var rows []entities.RWAQuotePriceEntity
	err := r.db.WithContext(ctx).Raw(
		`SELECT DISTINCT ON (side)
		        pair_id, side, ts, price, size
		   FROM rwa_quote_prices
		  WHERE pair_id = ?`+frag+`
		  ORDER BY side, ts DESC`,
		args...).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("query latest rwa_quote_prices: %w", err)
	}
	out := rwaEntitiesToPoints(rows, q.Source)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// QueryCandles serves chart reads for RWA pairs. Every supported interval
// resolves to a continuous aggregate (`rwa_quote_prices_{1m,1h,1d}`); raw
// `rwa_quote_prices` is never scanned on the hot path.
//
// AuxKey contract: orderbook side. The chart handler hardcodes `last`; the
// field stays on the wire so a future spread-analysis endpoint can use
// bid/ask without a repo refactor. Empty AuxKey is rejected.
//
// EntityKey is the decimal pair_id (matching `Save`/`Query`). Anything else
// is INVALID_ARGUMENT.
//
// Source mapping per interval:
//   - 1m / 1h / 1d → direct CA SELECT.
//   - 5m / 15m     → re-bucket from rwa_quote_prices_1m at query time.
//   - 4h           → re-bucket from rwa_quote_prices_1h at query time.
//
// ChartService.preflight rejects raw before it gets here.
func (r *RWAPriceRepository) QueryCandles(
	ctx context.Context,
	q apiprices.CandleQuery,
) ([]apiprices.Candle, error) {
	pid, err := strconv.ParseInt(q.EntityKey, 10, 64)
	if err != nil {
		return nil, coreerrors.InvalidArgument(
			"EntityKey must be a numeric pair_id, got: " + q.EntityKey)
	}
	side := strings.TrimSpace(q.AuxKey)
	if side == "" {
		return nil, coreerrors.InvalidArgument(`AuxKey (side) is required, e.g. "last"`)
	}

	src, ok := rwaCandleSource(q.Interval)
	if !ok {
		return nil, coreerrors.InvalidArgument(
			"Interval '" + string(q.Interval) + "' is not supported for RWA charts")
	}

	// view + rebucket spec are from a closed enum — safe to interpolate.
	where := "pair_id = ? AND side = ?"
	args := []any{pid, side}
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

	var rows []rwaCandleRow
	if err := r.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("query %s: %w", src.view, err)
	}

	out := make([]apiprices.Candle, len(rows))
	for i, rec := range rows {
		out[i] = apiprices.Candle{
			Bucket:  rec.Bucket.UTC(),
			Open:    rec.OpenPrice,
			High:    rec.HighPrice,
			Low:     rec.LowPrice,
			Close:   rec.ClosePrice,
			Samples: rec.Samples,
			// VolumeBase / VolumeQuote: nullable, deliberately left zero —
			// RWA traded-volume column lands in a follow-up via
			// orderbook_order ingestion (see ADR-0015).
		}
	}
	return out, nil
}

// Compile-time check: RWAPriceRepository satisfies the chart contract.
var _ apiprices.CandleRepository = (*RWAPriceRepository)(nil)

// rwaCandleSource maps an interval to its backing CA + optional re-bucket
// width. Direct intervals (1m / 1h / 1d) leave Rebucket empty; derived
// intervals (5m / 15m / 4h) carry the time_bucket() spec for the outer
// SELECT (see ADR-0015). Mirror of tokenCandleSource for the FA side.
func rwaCandleSource(iv apiprices.Interval) (candleSource, bool) {
	switch iv {
	case apiprices.Interval1m:
		return candleSource{view: "rwa_quote_prices_1m"}, true
	case apiprices.Interval5m:
		return candleSource{view: "rwa_quote_prices_1m", rebucket: "5 minutes"}, true
	case apiprices.Interval15m:
		return candleSource{view: "rwa_quote_prices_1m", rebucket: "15 minutes"}, true
	case apiprices.Interval1h:
		return candleSource{view: "rwa_quote_prices_1h"}, true
	case apiprices.Interval4h:
		return candleSource{view: "rwa_quote_prices_1h", rebucket: "4 hours"}, true
	case apiprices.Interval1d:
		return candleSource{view: "rwa_quote_prices_1d"}, true
	default:
		return candleSource{}, false
	}
}

// rwaCandleRow is the wire-compat row for the SELECT in QueryCandles. The
// CA columns are numeric in storage and decoded into shopspring/decimal so
// no float64 round-trips happen on the read path.
type rwaCandleRow struct {
	Bucket     time.Time       `gorm:"column:bucket"`
	OpenPrice  decimal.Decimal `gorm:"column:open_price"`
	HighPrice  decimal.Decimal `gorm:"column:high_price"`
	LowPrice   decimal.Decimal `gorm:"column:low_price"`
	ClosePrice decimal.Decimal `gorm:"column:close_price"`
	Samples    int64           `gorm:"column:samples"`
}

func rwaEntitiesToPoints(rows []entities.RWAQuotePriceEntity, source prices.Source) []prices.PricePoint {
	out := make([]prices.PricePoint, len(rows))
	for i, e := range rows {
		out[i] = prices.PricePoint{
			Source:    source,
			EntityKey: strconv.FormatInt(e.PairID, 10),
			Timestamp: e.Timestamp,
			Metric:    e.Side,
			Price:     e.Price,
			Size:      e.Size,
		}
	}
	return out
}

// AllTimeHighLast returns the highest price ever recorded for a pair on the
// given side, plus the day it occurred. It reads the 1d continuous aggregate
// (rwa_quote_prices_1d.high_price): the max daily high across all buckets is
// the all-time high, and that bucket is the ATH date. The read contract passes
// side="last". found=false when the pair has no aggregated rows yet.
func (r *RWAPriceRepository) AllTimeHighLast(
	ctx context.Context, pairID int64, side string,
) (decimal.Decimal, time.Time, bool, error) {
	var rows []struct {
		HighPrice decimal.Decimal `gorm:"column:high_price"`
		Bucket    time.Time       `gorm:"column:bucket"`
	}
	// The 1d CAGG stores the daily high as `max_price` (see migration 0010);
	// alias it to high_price so the scan struct binds. Mirrors the
	// max_price→high_price alias the chart SQL already uses (candle_sql.go).
	err := r.db.WithContext(ctx).Raw(
		`SELECT max_price AS high_price, bucket
		   FROM rwa_quote_prices_1d
		  WHERE pair_id = ? AND side = ?
		  ORDER BY max_price DESC
		  LIMIT 1`,
		pairID, side).Scan(&rows).Error
	if err != nil {
		return decimal.Decimal{}, time.Time{}, false, fmt.Errorf("query rwa all-time high: %w", err)
	}
	if len(rows) == 0 {
		return decimal.Decimal{}, time.Time{}, false, nil
	}
	return rows[0].HighPrice, rows[0].Bucket.UTC(), true, nil
}

// PriceAtOrBefore returns the most recent price at or before ts for a pair on
// the given side — the anchor for "price one year ago". It is a point lookup on
// raw rwa_quote_prices over the (pair_id, side, ts) index. found=false when no
// observation exists at/before ts (e.g. the pair is younger than the lookback).
func (r *RWAPriceRepository) PriceAtOrBefore(
	ctx context.Context, pairID int64, side string, ts time.Time,
) (decimal.Decimal, time.Time, bool, error) {
	var rows []struct {
		Price decimal.Decimal `gorm:"column:price"`
		TS    time.Time       `gorm:"column:ts"`
	}
	err := r.db.WithContext(ctx).Raw(
		`SELECT price, ts
		   FROM rwa_quote_prices
		  WHERE pair_id = ? AND side = ? AND ts <= ?
		  ORDER BY ts DESC
		  LIMIT 1`,
		pairID, side, ts.UTC()).Scan(&rows).Error
	if err != nil {
		return decimal.Decimal{}, time.Time{}, false, fmt.Errorf("query rwa price-at-or-before: %w", err)
	}
	if len(rows) == 0 {
		return decimal.Decimal{}, time.Time{}, false, nil
	}
	return rows[0].Price, rows[0].TS.UTC(), true, nil
}
