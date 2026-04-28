package entities

import (
	"time"

	"github.com/shopspring/decimal"
)

// RWAQuotePriceEntity is one row in the `rwa_quote_prices` hypertable.
// PK (PairID, Side, Timestamp). Size is nullable: indexer may not report volume.
type RWAQuotePriceEntity struct {
	PairID    int64            `gorm:"primaryKey;column:pair_id;not null"`
	Side      string           `gorm:"primaryKey;column:side;not null"`
	Timestamp time.Time        `gorm:"primaryKey;column:ts;not null"`
	Price     decimal.Decimal  `gorm:"column:price;type:numeric(38,18);not null"`
	Size      *decimal.Decimal `gorm:"column:size;type:numeric(38,18)"`
}

func (RWAQuotePriceEntity) TableName() string { return "rwa_quote_prices" }
