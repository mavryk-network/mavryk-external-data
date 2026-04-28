package entities

import (
	"time"

	"github.com/shopspring/decimal"
)

// TokenPriceEntity is one row in the long-format `token_prices` hypertable.
// Composite PK (TokenSymbol, SourceCode, QuoteCurrency, Timestamp).
type TokenPriceEntity struct {
	TokenSymbol   string          `gorm:"primaryKey;column:token_symbol;not null"`
	SourceCode    string          `gorm:"primaryKey;column:source_code;not null"`
	QuoteCurrency string          `gorm:"primaryKey;column:quote_currency;not null"`
	Timestamp     time.Time       `gorm:"primaryKey;column:ts;not null"`
	Price         decimal.Decimal `gorm:"column:price;type:numeric(38,18);not null"`
}

func (TokenPriceEntity) TableName() string { return "token_prices" }
