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
		QuoteAddr:       stringPtrOrNil(l.QuoteAddr),
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
	updateCols := []string{
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
	}
	// quote_addr updates only when the sync produced a value — same preserve
	// contract as rwa_pairs.quote_addr: a payment row missing its token ref in
	// a degraded response must not wipe a previously-good address.
	if l.QuoteAddr != "" {
		updateCols = append(updateCols, "quote_addr")
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_code"}, {Name: "token_addr"}},
		DoUpdates: clause.AssignmentColumns(updateCols),
	}).Create(&e)
	if res.Error != nil {
		return fmt.Errorf("upsert rwa_launches: %w", res.Error)
	}
	return nil
}

// DisableMissing soft-disables enabled launches of `source` whose token is not in
// `keepTokens`. Upsert never re-enables a row, so the keep set must be complete.
func (r *LaunchRepository) DisableMissing(ctx context.Context, source prices.Source, keepTokens []string) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&entities.RWALaunchEntity{}).
		Where("source_code = ? AND enabled = ?", string(source), true)
	if len(keepTokens) > 0 {
		tx = tx.Where("token_addr NOT IN ?", keepTokens)
	}
	res := tx.Update("enabled", false)
	if res.Error != nil {
		return 0, fmt.Errorf("disable missing rwa_launches: %w", res.Error)
	}
	return res.RowsAffected, nil
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
	quoteAddr := ""
	if e.QuoteAddr != nil {
		quoteAddr = *e.QuoteAddr
	}
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
		QuoteAddr:       quoteAddr,
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
