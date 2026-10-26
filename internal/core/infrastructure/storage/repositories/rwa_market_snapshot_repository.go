package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/domain/prices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The latest table is a bounded current-state cache. Complete observations
// are also archived without changing the legacy price or candle tables.
type RWAMarketSnapshotRepository struct{ db *gorm.DB }

func NewRWAMarketSnapshotRepository(db *gorm.DB) *RWAMarketSnapshotRepository {
	return &RWAMarketSnapshotRepository{db: db}
}

func (r *RWAMarketSnapshotRepository) SaveSnapshots(ctx context.Context, rows []prices.RWAMarketSnapshot) error {
	if len(rows) == 0 {
		return nil
	}
	// AssignmentColumns includes nulls, unlike GORM Updates(struct). One SQL
	// statement atomically replaces both sides, identity and execution time.
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Immutable exact-time evidence for history. A later backfill cannot
		// rewrite the book/execution provenance of an existing observation.
		if err := tx.Table("rwa_market_observations").Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
			return err
		}
		return tx.Table("rwa_market_snapshots").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "pair_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"network", "indexer_generation", "observed_at", "token_addr", "token_id",
				"quote_addr", "quote_token_id", "orderbook_addr", "native_quote", "reason",
				"bid", "ask", "bid_expires_at", "ask_expires_at", "last_price", "last_trade_at",
			}),
			Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
				SQL: "EXCLUDED.observed_at >= rwa_market_snapshots.observed_at",
			}}},
		}).Create(&rows).Error
	})
	if err != nil {
		return fmt.Errorf("save RWA market snapshots: %w", err)
	}
	return nil
}

func (r *RWAMarketSnapshotRepository) Snapshot(ctx context.Context, pairID int64) (prices.RWAMarketSnapshot, bool, error) {
	var row prices.RWAMarketSnapshot
	err := r.db.WithContext(ctx).Table("rwa_market_snapshots").Where("pair_id = ?", pairID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	return row, err == nil, err
}

// SnapshotsAt batch-loads only exact recorded instants. There is deliberately
// no nearest/as-of fallback that could attach a different book to a price row.
func (r *RWAMarketSnapshotRepository) SnapshotsAt(ctx context.Context, pairID int64, timestamps []time.Time) ([]prices.RWAMarketSnapshot, error) {
	if len(timestamps) == 0 {
		return nil, nil
	}
	var rows []prices.RWAMarketSnapshot
	err := r.db.WithContext(ctx).Table("rwa_market_observations").Where("pair_id = ? AND observed_at IN ?", pairID, timestamps).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("query RWA pricing observations: %w", err)
	}
	return rows, nil
}
