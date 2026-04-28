package repositories

import (
	"context"
	"fmt"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

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

// metricFilterFragment renders an optional `AND quote_currency = ANY(?)` fragment.
// Builds raw SQL because GORM can't compose Raw + Where conditionally.
func metricFilterFragment(metrics []string) string {
	if len(metrics) == 0 {
		return ""
	}
	return ` AND quote_currency = ANY(?)`
}

func rawArgs(q prices.Query) []any {
	args := []any{q.EntityKey, string(q.Source)}
	if len(q.Metrics) > 0 {
		args = append(args, q.Metrics)
	}
	return args
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
