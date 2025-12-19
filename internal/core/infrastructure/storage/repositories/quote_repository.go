package repositories

import (
	"context"
	"fmt"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/storage/entities"
	"strings"
	"time"

	"gorm.io/gorm"
)

// tokenNameToTableName maps token name to table name
// mvrk -> mvrk (table renamed from quotes to mvrk)
// other tokens -> use token name as table name
func tokenNameToTableName(tokenName string) string {
	tokenName = strings.ToLower(tokenName)
	// After migration, mvrk table is named mvrk (was quotes)
	return tokenName
}

type QuoteRepository struct {
	db *gorm.DB
}

func NewQuoteRepository(db *gorm.DB) *QuoteRepository {
	return &QuoteRepository{db: db}
}

// Save saves a quote for a specific token
func (r *QuoteRepository) Save(ctx context.Context, quote quotes.Quote, tokenName string) error {
	if !quotes.IsTokenSupported(tokenName) {
		return fmt.Errorf("token '%s' is not supported", tokenName)
	}

	tableName := fmt.Sprintf("mev.%s", tokenNameToTableName(tokenName))
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

	result := r.db.WithContext(ctx).Table(tableName).Create(entity)
	if result.Error != nil {
		return fmt.Errorf("failed to save quote for token %s: %w", tokenName, result.Error)
	}

	return nil
}

// SaveBatch saves a batch of quotes for a specific token
func (r *QuoteRepository) SaveBatch(ctx context.Context, quotesList []quotes.Quote, tokenName string) error {
	if len(quotesList) == 0 {
		return nil
	}

	if !quotes.IsTokenSupported(tokenName) {
		return fmt.Errorf("token '%s' is not supported", tokenName)
	}

	tableName := fmt.Sprintf("mev.%s", tokenNameToTableName(tokenName))
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

	result := r.db.WithContext(ctx).Table(tableName).CreateInBatches(quoteEntities, 100)
	if result.Error != nil {
		return fmt.Errorf("failed to save quotes batch for token %s: %w", tokenName, result.Error)
	}

	return nil
}

// GetLastQuote retrieves the last quote for a specific token
func (r *QuoteRepository) GetLastQuote(ctx context.Context, tokenName string) (quotes.Quote, error) {
	if !quotes.IsTokenSupported(tokenName) {
		return quotes.Quote{}, fmt.Errorf("token '%s' is not supported", tokenName)
	}

	tableName := fmt.Sprintf("mev.%s", tokenNameToTableName(tokenName))
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
func (r *QuoteRepository) GetQuotes(ctx context.Context, from, to time.Time, limit int, tokenName string) ([]quotes.Quote, error) {
	if !quotes.IsTokenSupported(tokenName) {
		return nil, fmt.Errorf("token '%s' is not supported", tokenName)
	}

	tableName := fmt.Sprintf("mev.%s", tokenNameToTableName(tokenName))
	var entities []entities.QuoteEntity
	
	query := r.db.WithContext(ctx).
		Table(tableName).
		Where("timestamp >= ? AND timestamp <= ?", from, to).
		Order("timestamp ASC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	result := query.Find(&entities)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get quotes for token %s: %w", tokenName, result.Error)
	}

	quotesList := make([]quotes.Quote, len(entities))
	for i, entity := range entities {
		quotesList[i] = r.entityToDomain(entity)
	}

	return quotesList, nil
}

// GetCount returns count of quotes for a specific token
func (r *QuoteRepository) GetCount(ctx context.Context, tokenName string) (int64, error) {
	if !quotes.IsTokenSupported(tokenName) {
		return 0, fmt.Errorf("token '%s' is not supported", tokenName)
	}

	tableName := fmt.Sprintf("mev.%s", tokenNameToTableName(tokenName))
	var count int64
	result := r.db.WithContext(ctx).Table(tableName).Count(&count)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to get quotes count for token %s: %w", tokenName, result.Error)
	}

	return count, nil
}

// GetLastTimestamp returns last timestamp for a specific token
func (r *QuoteRepository) GetLastTimestamp(ctx context.Context, tokenName string) (time.Time, error) {
	if !quotes.IsTokenSupported(tokenName) {
		return time.Time{}, fmt.Errorf("token '%s' is not supported", tokenName)
	}

	tableName := fmt.Sprintf("mev.%s", tokenNameToTableName(tokenName))
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
