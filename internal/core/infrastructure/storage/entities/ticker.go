package entities

import (
	"time"

	"github.com/shopspring/decimal"
)

// ExchangeEntity maps to the `exchanges` lookup table.
//
// `ID` is the CoinGecko market.identifier (e.g. "binance"). `Kind` is derived
// in code via tickers.ClassifyExchangeKind; CHECK constraint in 0012 keeps it
// to 'cex' / 'dex'. LastSeenAt is bumped on every job tick via UPSERT —
// useful operationally for spotting exchanges that delisted MVRK.
type ExchangeEntity struct {
	ID                  string    `gorm:"primaryKey;column:id"`
	Name                string    `gorm:"column:name;not null"`
	LogoURL             *string   `gorm:"column:logo_url"`
	Kind                string    `gorm:"column:kind;not null"`
	HasTradingIncentive bool      `gorm:"column:has_trading_incentive;not null"`
	LastSeenAt          time.Time `gorm:"column:last_seen_at;not null"`
}

func (ExchangeEntity) TableName() string { return "exchanges" }

// TokenTickerEntity is one row in the `token_tickers` hypertable.
//
// Composite PK (TokenSymbol, SourceCode, ExchangeID, TargetSymbol, Timestamp).
// Native units only — all FX conversion happens read-side via PriceConverter
// (ADR-0013). No `is_stale` column: derived from ts at read time.
type TokenTickerEntity struct {
	TokenSymbol     string           `gorm:"primaryKey;column:token_symbol;not null"`
	SourceCode      string           `gorm:"primaryKey;column:source_code;not null"`
	ExchangeID      string           `gorm:"primaryKey;column:exchange_id;not null"`
	TargetSymbol    string           `gorm:"primaryKey;column:target_symbol;not null"`
	Timestamp       time.Time        `gorm:"primaryKey;column:ts;not null"`
	LastPrice       decimal.Decimal  `gorm:"column:last_price;type:numeric(38,18);not null"`
	VolumeBase      *decimal.Decimal `gorm:"column:volume_24h_base;type:numeric(38,18)"`
	BidAskSpreadPct *decimal.Decimal `gorm:"column:bid_ask_spread_pct;type:numeric(20,10)"`
	TrustScore      *string          `gorm:"column:trust_score"`
	IsAnomaly       bool             `gorm:"column:is_anomaly;not null"`
	TradeURL        *string          `gorm:"column:trade_url"`
	LastTradedAt    *time.Time       `gorm:"column:last_traded_at"`
}

func (TokenTickerEntity) TableName() string { return "token_tickers" }
