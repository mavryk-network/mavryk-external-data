package entities

import "time"

// BackfillStateEntity tracks per-(source, entity_key) cursor + error/backoff state
// for the reverse-backfill jobs. PK is composite: same row holds state for an FT
// token under coingecko OR for an RWA pair under a future indexer.
//
// CursorID / CursorTs form the keyset cursor for forward-walking sources.
// Equiteez paginates filled orders by (ended_at, id): CursorTs is the fill time
// of the last ingested order and CursorID the tie-break for fills sharing it.
// Ordering by id alone would permanently skip a limit order that fills after the
// cursor passed its (creation-time) id. CoinGecko backfill leaves both NULL.
type BackfillStateEntity struct {
	SourceCode     string     `gorm:"primaryKey;column:source_code;not null"`
	EntityKey      string     `gorm:"primaryKey;column:entity_key;not null"`
	OldestTs       *time.Time `gorm:"column:oldest_ts"`
	CursorID       *int64     `gorm:"column:cursor_id"`
	CursorTs       *time.Time `gorm:"column:cursor_ts"`
	Disabled       bool       `gorm:"column:disabled;not null;default:false"`
	DisabledReason string     `gorm:"column:disabled_reason;not null;default:''"`
	ErrorCount     int        `gorm:"column:error_count;not null;default:0"`
	LastError      string     `gorm:"column:last_error;not null;default:''"`
	NextAttemptAt  *time.Time `gorm:"column:next_attempt_at"`
	CreatedAt      time.Time  `gorm:"column:created_at;<-:false"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null"`
}

func (BackfillStateEntity) TableName() string { return "backfill_state" }
