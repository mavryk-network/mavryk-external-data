package entities

import "time"

// SourceEntity maps to the `sources` lookup table (registry of upstream providers).
type SourceEntity struct {
	Code      string    `gorm:"primaryKey;column:code"`
	Name      string    `gorm:"column:name;not null"`
	Kind      string    `gorm:"column:kind;not null"`
	CreatedAt time.Time `gorm:"column:created_at;<-:false"`
}

func (SourceEntity) TableName() string { return "sources" }

// TokenEntity maps to the `tokens` lookup table.
type TokenEntity struct {
	Symbol    string    `gorm:"primaryKey;column:symbol"`
	Name      string    `gorm:"column:name;not null"`
	Decimals  int16     `gorm:"column:decimals;not null"`
	CGID      *string   `gorm:"column:cg_id"`
	Enabled   bool      `gorm:"column:enabled;not null"`
	CreatedAt time.Time `gorm:"column:created_at;<-:false"`
}

func (TokenEntity) TableName() string { return "tokens" }

// RWAPairEntity maps to the `rwa_pairs` lookup table.
//
// The natural key is `(source_code, orderbook_addr)` — orderbook contracts are
// unique per source. Tokens may own multiple orderbooks (different quote
// currencies), so `token_addr` is not unique on its own.
type RWAPairEntity struct {
	ID            int64      `gorm:"primaryKey;column:id"`
	BaseSymbol    string     `gorm:"column:base_symbol;not null"`
	QuoteSymbol   string     `gorm:"column:quote_symbol;not null"`
	SourceCode    string     `gorm:"column:source_code;not null"`
	TokenAddr     *string    `gorm:"column:token_addr"`
	OrderbookAddr *string    `gorm:"column:orderbook_addr"`
	Enabled       bool       `gorm:"column:enabled;not null"`
	LastSyncedAt  *time.Time `gorm:"column:last_synced_at"`
	CreatedAt     time.Time  `gorm:"column:created_at;<-:false"`
}

func (RWAPairEntity) TableName() string { return "rwa_pairs" }
