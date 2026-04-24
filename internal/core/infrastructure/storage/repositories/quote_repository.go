package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/storage/entities"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type QuoteRepository struct {
	db *gorm.DB
}

func NewQuoteRepository(db *gorm.DB) *QuoteRepository {
	return &QuoteRepository{db: db}
}

// validateToken maps the incoming token name to the canonical domain Token.
// Repository never builds SQL identifiers from user input; `token` is always a parameter.
func validateToken(name string) (string, error) {
	t, ok := quotes.NormalizeToken(name)
	if !ok {
		return "", fmt.Errorf("token %q is not supported", name)
	}
	return string(t), nil
}

func quoteInsertOnConflict() clause.OnConflict {
	return clause.OnConflict{
		Columns:   []clause.Column{{Name: "token"}, {Name: "timestamp"}},
		DoNothing: true,
	}
}

// Save inserts a single quote for the given token. Duplicate (token, timestamp) is skipped.
func (r *QuoteRepository) Save(ctx context.Context, quote quotes.Quote, tokenName string) error {
	token, err := validateToken(tokenName)
	if err != nil {
		return err
	}
	entity := entities.QuoteEntity{
		Token:     token,
		Timestamp: quote.Timestamp,
		BTC:       quote.BTC,
		USD:       quote.USD,
		EUR:       quote.EUR,
		CNY:       quote.CNY,
		JPY:       quote.JPY,
		KRW:       quote.KRW,
		ETH:       quote.ETH,
		GBP:       quote.GBP,
	}
	result := r.db.WithContext(ctx).Clauses(quoteInsertOnConflict()).Create(&entity)
	if result.Error != nil {
		return fmt.Errorf("failed to save quote for token %s: %w", token, result.Error)
	}
	return nil
}

// SaveBatch inserts quotes in batches. Duplicate (token, timestamp) rows are skipped.
// Returns the number of rows actually inserted.
func (r *QuoteRepository) SaveBatch(ctx context.Context, quotesList []quotes.Quote, tokenName string) (int64, error) {
	if len(quotesList) == 0 {
		return 0, nil
	}
	token, err := validateToken(tokenName)
	if err != nil {
		return 0, err
	}

	quoteEntities := make([]entities.QuoteEntity, len(quotesList))
	for i, q := range quotesList {
		quoteEntities[i] = entities.QuoteEntity{
			Token:     token,
			Timestamp: q.Timestamp,
			BTC:       q.BTC,
			USD:       q.USD,
			EUR:       q.EUR,
			CNY:       q.CNY,
			JPY:       q.JPY,
			KRW:       q.KRW,
			ETH:       q.ETH,
			GBP:       q.GBP,
		}
	}

	result := r.db.WithContext(ctx).Clauses(quoteInsertOnConflict()).CreateInBatches(quoteEntities, 100)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to save quotes batch for token %s: %w", token, result.Error)
	}
	return result.RowsAffected, nil
}

// GetLastQuote retrieves the latest quote for a specific token.
func (r *QuoteRepository) GetLastQuote(ctx context.Context, tokenName string) (quotes.Quote, error) {
	token, err := validateToken(tokenName)
	if err != nil {
		return quotes.Quote{}, err
	}
	var entity entities.QuoteEntity
	result := r.db.WithContext(ctx).
		Where("token = ?", token).
		Order("timestamp DESC").
		Take(&entity)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return quotes.Quote{}, fmt.Errorf("no quotes found for token '%s'", token)
		}
		return quotes.Quote{}, fmt.Errorf("failed to get last quote for token %s: %w", token, result.Error)
	}
	return entityToDomain(entity), nil
}

// GetQuotes retrieves quotes for a specific token in a time range.
// If both from and to are zero, returns the latest quotes up to limit.
func (r *QuoteRepository) GetQuotes(ctx context.Context, from, to time.Time, limit int, tokenName string) ([]quotes.Quote, error) {
	token, err := validateToken(tokenName)
	if err != nil {
		return nil, err
	}

	var rows []entities.QuoteEntity
	query := r.db.WithContext(ctx).Model(&entities.QuoteEntity{}).Where("token = ?", token)

	reverse := false
	if from.IsZero() && to.IsZero() {
		query = query.Order("timestamp DESC")
		reverse = true
	} else {
		query = query.Where("timestamp >= ? AND timestamp <= ?", from, to).Order("timestamp ASC")
	}
	if limit > 0 {
		query = query.Limit(limit)
	}

	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get quotes for token %s: %w", token, err)
	}

	result := make([]quotes.Quote, len(rows))
	for i, e := range rows {
		result[i] = entityToDomain(e)
	}

	if reverse {
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}
	return result, nil
}

// GetCount returns total number of rows for a specific token.
func (r *QuoteRepository) GetCount(ctx context.Context, tokenName string) (int64, error) {
	token, err := validateToken(tokenName)
	if err != nil {
		return 0, err
	}
	var count int64
	result := r.db.WithContext(ctx).
		Model(&entities.QuoteEntity{}).
		Where("token = ?", token).
		Count(&count)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to get quotes count for token %s: %w", token, result.Error)
	}
	return count, nil
}

// GetOldestTimestamp returns the earliest timestamp for a specific token.
// Used by the backfill job to seed its oldest_ts cursor when backfill_state.oldest_ts is NULL.
func (r *QuoteRepository) GetOldestTimestamp(ctx context.Context, tokenName string) (time.Time, error) {
	token, err := validateToken(tokenName)
	if err != nil {
		return time.Time{}, err
	}
	var entity entities.QuoteEntity
	result := r.db.WithContext(ctx).
		Select("timestamp").
		Where("token = ?", token).
		Order("timestamp ASC").
		Take(&entity)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return time.Time{}, fmt.Errorf("no quotes found for token '%s'", token)
		}
		return time.Time{}, fmt.Errorf("failed to get oldest timestamp for token %s: %w", token, result.Error)
	}
	return entity.Timestamp, nil
}

// GetLastTimestamp returns the latest timestamp for a specific token.
func (r *QuoteRepository) GetLastTimestamp(ctx context.Context, tokenName string) (time.Time, error) {
	token, err := validateToken(tokenName)
	if err != nil {
		return time.Time{}, err
	}
	var entity entities.QuoteEntity
	result := r.db.WithContext(ctx).
		Select("timestamp").
		Where("token = ?", token).
		Order("timestamp DESC").
		Take(&entity)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return time.Time{}, fmt.Errorf("no quotes found for token '%s'", token)
		}
		return time.Time{}, fmt.Errorf("failed to get last timestamp for token %s: %w", token, result.Error)
	}
	return entity.Timestamp, nil
}

func entityToDomain(entity entities.QuoteEntity) quotes.Quote {
	return quotes.Quote{
		Timestamp: entity.Timestamp,
		BTC:       entity.BTC,
		USD:       entity.USD,
		EUR:       entity.EUR,
		CNY:       entity.CNY,
		JPY:       entity.JPY,
		KRW:       entity.KRW,
		ETH:       entity.ETH,
		GBP:       entity.GBP,
	}
}
