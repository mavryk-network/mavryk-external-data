package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

	"gorm.io/gorm"
)

// LookupRepository serves the lookup tables (sources, tokens, rwa_pairs).
// Reads are unrestricted; writes are limited to the controlled mutations the
// RWA sync job needs (`UpsertRWAPair`, `DisableMissingRWAPairs`). Bulk loads
// of `tokens`/`sources` continue to be migration-driven.
type LookupRepository struct {
	db *gorm.DB
}

func NewLookupRepository(db *gorm.DB) *LookupRepository {
	return &LookupRepository{db: db}
}

// Tokens loads every row from `tokens`. Used at startup to populate the in-process
// token registry; never called on the hot path.
func (r *LookupRepository) Tokens(ctx context.Context) ([]prices.TokenInfo, error) {
	var rows []entities.TokenEntity
	if err := r.db.WithContext(ctx).Order("symbol").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load tokens: %w", err)
	}
	out := make([]prices.TokenInfo, 0, len(rows))
	for _, e := range rows {
		info := prices.TokenInfo{
			Symbol:   prices.Token(e.Symbol),
			Name:     e.Name,
			Decimals: int(e.Decimals),
			Enabled:  e.Enabled,
		}
		if e.CGID != nil {
			info.CoinGeckoID = *e.CGID
		}
		out = append(out, info)
	}
	return out, nil
}

// RWAPairs loads every row from `rwa_pairs`. Filtering is done in-memory by callers.
func (r *LookupRepository) RWAPairs(ctx context.Context) ([]prices.RWAPair, error) {
	var rows []entities.RWAPairEntity
	if err := r.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load rwa_pairs: %w", err)
	}
	out := make([]prices.RWAPair, 0, len(rows))
	for _, e := range rows {
		out = append(out, entityToRWAPair(e))
	}
	return out, nil
}

// LookupRWAPair returns one pair by ID, or ErrPairNotFound.
func (r *LookupRepository) LookupRWAPair(ctx context.Context, id int64) (prices.RWAPair, error) {
	var e entities.RWAPairEntity
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&e).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return prices.RWAPair{}, prices.ErrPairNotFound
		}
		return prices.RWAPair{}, fmt.Errorf("lookup rwa_pair %d: %w", id, err)
	}
	return entityToRWAPair(e), nil
}

// UpsertRWAPair finds-or-creates by `(source_code, orderbook_addr)` and
// updates metadata + `last_synced_at`. Returns the persisted ID.
//
// Operator overrides survive sync: `enabled` is **only set on INSERT** (to
// true). Existing pairs keep whatever the operator chose. To reset a manual
// override, edit the row in DB.
func (r *LookupRepository) UpsertRWAPair(ctx context.Context, p prices.RWAPair, syncedAt time.Time) (int64, error) {
	if p.Source == "" || p.OrderbookAddr == "" {
		return 0, fmt.Errorf("upsert rwa_pair: source and orderbook_addr are required")
	}

	tokenAddr := stringPtrOrNil(p.TokenAddr)
	orderbookAddr := stringPtrOrNil(p.OrderbookAddr)

	tx := r.db.WithContext(ctx)

	var existing entities.RWAPairEntity
	res := tx.Where("source_code = ? AND orderbook_addr = ?", string(p.Source), p.OrderbookAddr).Take(&existing)
	switch {
	case errors.Is(res.Error, gorm.ErrRecordNotFound):
		row := entities.RWAPairEntity{
			BaseSymbol:    p.BaseSymbol,
			QuoteSymbol:   p.QuoteSymbol,
			SourceCode:    string(p.Source),
			TokenAddr:     tokenAddr,
			OrderbookAddr: orderbookAddr,
			Enabled:       true,
			LastSyncedAt:  &syncedAt,
		}
		if err := tx.Create(&row).Error; err != nil {
			return 0, fmt.Errorf("insert rwa_pair: %w", err)
		}
		return row.ID, nil
	case res.Error != nil:
		return 0, fmt.Errorf("lookup rwa_pair: %w", res.Error)
	default:
		// Update metadata only — leave `enabled` for the operator to control.
		err := tx.Model(&entities.RWAPairEntity{}).
			Where("id = ?", existing.ID).
			Updates(map[string]any{
				"base_symbol":    p.BaseSymbol,
				"quote_symbol":   p.QuoteSymbol,
				"token_addr":     tokenAddr,
				"last_synced_at": syncedAt,
			}).Error
		if err != nil {
			return 0, fmt.Errorf("update rwa_pair: %w", err)
		}
		return existing.ID, nil
	}
}

// DisableMissingRWAPairs marks every pair for `source` whose ID is NOT in
// `keepIDs` as `enabled=false`. Used by the sync to handle pairs that fell
// out of the upstream allowlist.
//
// Pairs already disabled stay disabled. Pairs in `keepIDs` are not touched
// (operator overrides are preserved).
func (r *LookupRepository) DisableMissingRWAPairs(ctx context.Context, source prices.Source, keepIDs []int64) (int64, error) {
	tx := r.db.WithContext(ctx).Model(&entities.RWAPairEntity{}).
		Where("source_code = ? AND enabled = ?", string(source), true)
	if len(keepIDs) > 0 {
		tx = tx.Where("id NOT IN ?", keepIDs)
	}
	res := tx.Update("enabled", false)
	if res.Error != nil {
		return 0, fmt.Errorf("disable missing rwa_pairs: %w", res.Error)
	}
	return res.RowsAffected, nil
}

func entityToRWAPair(e entities.RWAPairEntity) prices.RWAPair {
	pair := prices.RWAPair{
		ID:           e.ID,
		BaseSymbol:   e.BaseSymbol,
		QuoteSymbol:  e.QuoteSymbol,
		Source:       prices.Source(e.SourceCode),
		Enabled:      e.Enabled,
		LastSyncedAt: e.LastSyncedAt,
	}
	if e.TokenAddr != nil {
		pair.TokenAddr = *e.TokenAddr
	}
	if e.OrderbookAddr != nil {
		pair.OrderbookAddr = *e.OrderbookAddr
	}
	return pair
}

func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}
