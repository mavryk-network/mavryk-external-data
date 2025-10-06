package entities

import (
	"gorm.io/gorm"
	"time"
)

type QuoteEntity struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	Timestamp time.Time      `gorm:"not null;index" json:"timestamp"`
	BTC       float64        `gorm:"type:decimal(20,8)" json:"btc"`
	USD       float64        `gorm:"type:decimal(20,8)" json:"usd"`
	EUR       float64        `gorm:"type:decimal(20,8)" json:"eur"`
	CNY       float64        `gorm:"type:decimal(20,8)" json:"cny"`
	JPY       float64        `gorm:"type:decimal(20,8)" json:"jpy"`
	KRW       float64        `gorm:"type:decimal(20,8)" json:"krw"`
	ETH       float64        `gorm:"type:decimal(20,8)" json:"eth"`
	GBP       float64        `gorm:"type:decimal(20,8)" json:"gbp"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

func (QuoteEntity) TableName() string {
	return "mvkt.quotes"
}
