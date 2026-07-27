package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LaunchRepository owns `rwa_launches`: the primary-issuance state the sync job
// writes and GET /v1/rwa reads.
type LaunchRepository struct {
	db *gorm.DB
}

func NewLaunchRepository(db *gorm.DB) *LaunchRepository {
	return &LaunchRepository{db: db}
}

// Upsert writes one launch, creating the row on first sight.
//
// `enabled` is set only on INSERT and never overwritten on conflict — same
// contract as LookupRepository.UpsertRWAPair: it is the operator's kill-switch,
// so a routine sync must not resurrect an asset someone deliberately hid.
func (r *LaunchRepository) Upsert(ctx context.Context, l prices.RWALaunch, now time.Time) error {
	if l.Source == "" || l.TokenAddr == "" {
		return fmt.Errorf("launch source and token_addr are required")
	}
	e := entities.RWALaunchEntity{
		SourceCode:      string(l.Source),
		TokenAddr:       l.TokenAddr,
		TokenID:         l.TokenID,
		LaunchID:        l.LaunchID,
		Name:            l.Name,
		Status:          l.Status,
		Active:          l.Active,
		BaseSymbol:      l.BaseSymbol,
		QuoteSymbol:     l.QuoteSymbol,
		Price:           decimal.NullDecimal{Decimal: l.Price, Valid: !l.Price.IsZero()},
		TotalBought:     decimal.NullDecimal{Decimal: l.TotalBought, Valid: true},
		MaxAmountCap:    decimal.NullDecimal{Decimal: l.MaxAmountCap, Valid: true},
		ProgressPercent: l.ProgressPercent,
		SaleStart:       l.SaleStart,
		SaleEnd:         l.SaleEnd,
		SaleClosed:      l.SaleClosed,
		Enabled:         true, // insert-only; see DoUpdates below
		LastSyncedAt:    now.UTC(),
		UpdatedAt:       now.UTC(),
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_code"}, {Name: "token_addr"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"token_id",
			"launch_id",
			"name",
			"status",
			"active",
			"base_symbol",
			"quote_symbol",
			"price",
			"total_bought",
			"max_amount_cap",
			"progress_percent",
			"sale_start",
			"sale_end",
			"sale_closed",
			"last_synced_at",
			"updated_at",
			// NOTE: `enabled` intentionally omitted — operator-owned.
		}),
	}).Create(&e)
	if res.Error != nil {
		return fmt.Errorf("upsert rwa_launches: %w", res.Error)
	}
	return nil
}

// EnabledLaunches returns every enabled launch for `source`, ordered by symbol
// so the API response is deterministic.
func (r *LaunchRepository) EnabledLaunches(ctx context.Context, source prices.Source) ([]prices.RWALaunch, error) {
	var rows []entities.RWALaunchEntity
	err := r.db.WithContext(ctx).
		Where("source_code = ? AND enabled", string(source)).
		Order("base_symbol ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list rwa_launches: %w", err)
	}
	out := make([]prices.RWALaunch, 0, len(rows))
	for _, e := range rows {
		out = append(out, entityToLaunch(e))
	}
	return out, nil
}

// LaunchBySymbol resolves a `{base}-{quote}` symbol to its enabled launch.
// Comparison is case-insensitive to match the URL parsing, which lowercases.
// found=false (not an error) when the symbol is not a primary-market asset, so
// the caller can keep its own 404.
func (r *LaunchRepository) LaunchBySymbol(ctx context.Context, source prices.Source, base, quote string) (prices.RWALaunch, bool, error) {
	var e entities.RWALaunchEntity
	err := r.db.WithContext(ctx).
		Where("source_code = ? AND enabled AND lower(base_symbol) = lower(?) AND lower(quote_symbol) = lower(?)",
			string(source), base, quote).
		Take(&e).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return prices.RWALaunch{}, false, nil
		}
		return prices.RWALaunch{}, false, fmt.Errorf("lookup rwa_launch by symbol: %w", err)
	}
	return entityToLaunch(e), true, nil
}

func entityToLaunch(e entities.RWALaunchEntity) prices.RWALaunch {
	return prices.RWALaunch{
		Source:          prices.Source(e.SourceCode),
		TokenAddr:       e.TokenAddr,
		TokenID:         e.TokenID,
		LaunchID:        e.LaunchID,
		Name:            e.Name,
		Status:          e.Status,
		Active:          e.Active,
		BaseSymbol:      e.BaseSymbol,
		QuoteSymbol:     e.QuoteSymbol,
		Price:           e.Price.Decimal,
		TotalBought:     e.TotalBought.Decimal,
		MaxAmountCap:    e.MaxAmountCap.Decimal,
		ProgressPercent: e.ProgressPercent,
		SaleStart:       e.SaleStart,
		SaleEnd:         e.SaleEnd,
		SaleClosed:      e.SaleClosed,
		LastSyncedAt:    e.LastSyncedAt,
		Enabled:         e.Enabled,
	}
}
