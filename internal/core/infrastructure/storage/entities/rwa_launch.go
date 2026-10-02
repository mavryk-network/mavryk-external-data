package entities

import (
	"time"

	"github.com/shopspring/decimal"
)

// RWALaunchEntity mirrors `rwa_launches` — the surfaced primary-issuance state
// of one RWA token (see migration 0016). PK is (source_code, token_addr).
//
// TotalBought / MaxAmountCap are NUMERIC(78,0) raw on-chain nats and Price is
// NUMERIC(38,18); all three map to decimal.Decimal so supply-scale values never
// round-trip through float64.
type RWALaunchEntity struct {
	SourceCode string `gorm:"primaryKey;column:source_code;not null"`
	TokenAddr  string `gorm:"primaryKey;column:token_addr;not null"`
	TokenID    int    `gorm:"column:token_id;not null;default:0"`

	LaunchID int    `gorm:"column:launch_id;not null"`
	Name     string `gorm:"column:name;not null;default:''"`
	Status   string `gorm:"column:status;not null;default:''"`
	Active   bool   `gorm:"column:active;not null;default:false"`

	BaseSymbol      string              `gorm:"column:base_symbol;not null;default:''"`
	QuoteSymbol     string              `gorm:"column:quote_symbol;not null;default:''"`
	QuoteAddr       *string             `gorm:"column:quote_addr"`
	Price           decimal.NullDecimal `gorm:"column:price"`
	TotalBought     decimal.NullDecimal `gorm:"column:total_bought"`
	MaxAmountCap    decimal.NullDecimal `gorm:"column:max_amount_cap"`
	ProgressPercent float64             `gorm:"column:progress_percent;not null;default:0"`

	SaleStart  *time.Time `gorm:"column:sale_start"`
	SaleEnd    *time.Time `gorm:"column:sale_end"`
	SaleClosed *time.Time `gorm:"column:sale_closed"`

	Enabled        bool      `gorm:"column:enabled;not null;default:true"`
	DisabledReason *string   `gorm:"column:disabled_reason"`
	LastSyncedAt   time.Time `gorm:"column:last_synced_at;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;<-:false"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null"`
}

func (RWALaunchEntity) TableName() string { return "rwa_launches" }
