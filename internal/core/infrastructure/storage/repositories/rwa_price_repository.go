package repositories

import (
	"context"
	"fmt"
	"strconv"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

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
	args := []any{pairID}
	frag := ""
	if q.HasMetricFilter() {
		frag = ` AND side = ANY(?)`
		args = append(args, q.Metrics)
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
