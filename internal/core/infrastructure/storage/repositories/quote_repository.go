package repositories

import (
	"context"
	"fmt"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/storage/entities"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type QuoteRepository struct {
	db *gorm.DB
}

func NewQuoteRepository(db *gorm.DB) *QuoteRepository {
	return &QuoteRepository{db: db}
}

// Save saves a quote for a specific token
func (r *QuoteRepository) Save(ctx context.Context, quote quotes.Quote, tokenName string) error {
	tableName, err := quotes.QuoteHypertableQualifiedName(tokenName)
	if err != nil {
		return err
	}
	entity := &entities.QuoteEntity{
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

	result := r.db.WithContext(ctx).Table(tableName).Clauses(quoteInsertOnConflict()).Create(entity)
	if result.Error != nil {
		return fmt.Errorf("failed to save quote for token %s: %w", tokenName, result.Error)
	}

	return nil
}

func quoteInsertOnConflict() clause.OnConflict {
	return clause.OnConflict{
		Columns:   []clause.Column{{Name: "timestamp"}},
		DoNothing: true,
	}
}

// SaveBatch inserts quotes in batches. Duplicate timestamps are skipped (requires unique index on timestamp — see migration 004).
// Returns the number of rows inserted (excludes conflicts).
func (r *QuoteRepository) SaveBatch(ctx context.Context, quotesList []quotes.Quote, tokenName string) (int64, error) {
	if len(quotesList) == 0 {
		return 0, nil
	}

	tableName, err := quotes.QuoteHypertableQualifiedName(tokenName)
	if err != nil {
		return 0, err
	}

	quoteEntities := make([]entities.QuoteEntity, len(quotesList))
	for i, quote := range quotesList {
		quoteEntities[i] = entities.QuoteEntity{
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
	}

	result := r.db.WithContext(ctx).Table(tableName).Clauses(quoteInsertOnConflict()).CreateInBatches(quoteEntities, 100)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to save quotes batch for token %s: %w", tokenName, result.Error)
	}

	return result.RowsAffected, nil
}

// GetLastQuote retrieves the last quote for a specific token
func (r *QuoteRepository) GetLastQuote(ctx context.Context, tokenName string) (quotes.Quote, error) {
	tableName, err := quotes.QuoteHypertableQualifiedName(tokenName)
	if err != nil {
		return quotes.Quote{}, err
	}
	var entity entities.QuoteEntity

	result := r.db.WithContext(ctx).
		Table(tableName).
		Order("timestamp DESC").
		First(&entity)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return quotes.Quote{}, fmt.Errorf("no quotes found for token '%s'", tokenName)
		}
		return quotes.Quote{}, fmt.Errorf("failed to get last quote for token %s: %w", tokenName, result.Error)
	}

	return r.entityToDomain(entity), nil
}

// GetQuotes retrieves quotes for a specific token
// If from and to are zero times, returns latest quotes up to limit
// Otherwise, returns quotes within the time range
func (r *QuoteRepository) GetQuotes(ctx context.Context, from, to time.Time, limit int, tokenName string) ([]quotes.Quote, error) {
	tableName, err := quotes.QuoteHypertableQualifiedName(tokenName)
	if err != nil {
		return nil, err
	}
	var entities []entities.QuoteEntity

	query := r.db.WithContext(ctx).Table(tableName)

	// If from and to are zero (not set), get latest quotes without time filter
	if from.IsZero() && to.IsZero() {
		query = query.Order("timestamp DESC")
		if limit > 0 {
			query = query.Limit(limit)
		}
	} else {
		// Use time range filter
		query = query.
			Where("timestamp >= ? AND timestamp <= ?", from, to).
			Order("timestamp ASC")
		if limit > 0 {
			query = query.Limit(limit)
		}
	}

	result := query.Find(&entities)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get quotes for token %s: %w", tokenName, result.Error)
	}

	quotesList := make([]quotes.Quote, len(entities))
	for i, entity := range entities {
		quotesList[i] = r.entityToDomain(entity)
	}

	// Reverse order if we got latest quotes (DESC -> ASC for response)
	if from.IsZero() && to.IsZero() {
		for i, j := 0, len(quotesList)-1; i < j; i, j = i+1, j-1 {
			quotesList[i], quotesList[j] = quotesList[j], quotesList[i]
		}
	}

	return quotesList, nil
}

// GetCount returns count of quotes for a specific token
func (r *QuoteRepository) GetCount(ctx context.Context, tokenName string) (int64, error) {
	tableName, err := quotes.QuoteHypertableQualifiedName(tokenName)
	if err != nil {
		return 0, err
	}
	var count int64
	result := r.db.WithContext(ctx).Table(tableName).Count(&count)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to get quotes count for token %s: %w", tokenName, result.Error)
	}

	return count, nil
}

// GetLastTimestamp returns last timestamp for a specific token
func (r *QuoteRepository) GetLastTimestamp(ctx context.Context, tokenName string) (time.Time, error) {
	tableName, err := quotes.QuoteHypertableQualifiedName(tokenName)
	if err != nil {
		return time.Time{}, err
	}
	var entity entities.QuoteEntity
	result := r.db.WithContext(ctx).
		Table(tableName).
		Select("timestamp").
		Order("timestamp DESC").
		First(&entity)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return time.Time{}, fmt.Errorf("no quotes found for token '%s'", tokenName)
		}
		return time.Time{}, fmt.Errorf("failed to get last timestamp for token %s: %w", tokenName, result.Error)
	}

	return entity.Timestamp, nil
}

func (r *QuoteRepository) entityToDomain(entity entities.QuoteEntity) quotes.Quote {
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
