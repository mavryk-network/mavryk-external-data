package entities

import "time"

// BackfillStateEntity tracks per-token state for the reverse-backfill job.
// Inserted lazily on first step; upserted after every step (success or error).
type BackfillStateEntity struct {
	Token          string     `gorm:"primaryKey;type:text;not null;column:token" json:"token"`
	OldestTs       *time.Time `gorm:"column:oldest_ts" json:"oldest_ts,omitempty"`
	Disabled       bool       `gorm:"column:disabled;not null;default:false" json:"disabled"`
	DisabledReason string     `gorm:"column:disabled_reason;not null;default:''" json:"disabled_reason"`
	ErrorCount     int        `gorm:"column:error_count;not null;default:0" json:"error_count"`
	LastError      string     `gorm:"column:last_error;not null;default:''" json:"last_error"`
	NextAttemptAt  *time.Time `gorm:"column:next_attempt_at" json:"next_attempt_at,omitempty"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;not null" json:"updated_at"`
}

// TableName maps the GORM model to the migrations-managed table.
func (BackfillStateEntity) TableName() string {
	return "backfill_state"
}
