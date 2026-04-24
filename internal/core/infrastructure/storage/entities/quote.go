package entities

import "time"

// QuoteEntity is one row in the unified `quotes` hypertable.
// Composite primary key is (Token, Timestamp); CreatedAt is set by the DB (DEFAULT NOW()).
type QuoteEntity struct {
	Token     string    `gorm:"primaryKey;type:text;not null;column:token" json:"token"`
	Timestamp time.Time `gorm:"primaryKey;not null;column:timestamp" json:"timestamp"`
	BTC       float64   `gorm:"type:decimal(20,8);column:btc" json:"btc"`
	USD       float64   `gorm:"type:decimal(20,8);column:usd" json:"usd"`
	EUR       float64   `gorm:"type:decimal(20,8);column:eur" json:"eur"`
	CNY       float64   `gorm:"type:decimal(20,8);column:cny" json:"cny"`
	JPY       float64   `gorm:"type:decimal(20,8);column:jpy" json:"jpy"`
	KRW       float64   `gorm:"type:decimal(20,8);column:krw" json:"krw"`
	ETH       float64   `gorm:"type:decimal(20,8);column:eth" json:"eth"`
	GBP       float64   `gorm:"type:decimal(20,8);column:gbp" json:"gbp"`
	CreatedAt time.Time `gorm:"column:created_at;<-:false" json:"created_at"`
}

// TableName returns the single unified table name; no schema prefix.
func (QuoteEntity) TableName() string {
	return "quotes"
}
