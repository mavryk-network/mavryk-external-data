package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/infrastructure/storage/entities"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BackfillState is the domain-facing view of a backfill_state row.
// Pointer fields distinguish "unset" (NULL in Postgres) from "zero time / empty".
type BackfillState struct {
	Token          string
	OldestTs       *time.Time
	Disabled       bool
	DisabledReason string
	ErrorCount     int
	LastError      string
	NextAttemptAt  *time.Time
	UpdatedAt      time.Time
}

// Disabled-reason constants. Kept in one place so job and metrics labels agree.
const (
	BackfillDisabledReasonReachedStartFrom = "reached_start_from"
	BackfillDisabledReasonAutoDisabled     = "auto_disabled"
	BackfillDisabledReasonManual           = "manual"
)

type BackfillStateRepository struct {
	db *gorm.DB
}

func NewBackfillStateRepository(db *gorm.DB) *BackfillStateRepository {
	return &BackfillStateRepository{db: db}
}

// Get returns the row for a token, or (nil, nil) when there is no row yet.
// Any other DB error is surfaced to the caller.
func (r *BackfillStateRepository) Get(ctx context.Context, token string) (*BackfillState, error) {
	if token == "" {
		return nil, fmt.Errorf("token is required")
	}
	var e entities.BackfillStateEntity
	res := r.db.WithContext(ctx).Where("token = ?", token).Take(&e)
	if res.Error != nil {
		if errors.Is(res.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get backfill_state for %s: %w", token, res.Error)
	}
	return entityToState(&e), nil
}

// Upsert writes the current state, creating the row on first step.
// updated_at is always stamped to now(UTC).
func (r *BackfillStateRepository) Upsert(ctx context.Context, s *BackfillState) error {
	if s == nil {
		return fmt.Errorf("state is required")
	}
	if s.Token == "" {
		return fmt.Errorf("state.token is required")
	}
	e := stateToEntity(s)
	e.UpdatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "token"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"oldest_ts",
			"disabled",
			"disabled_reason",
			"error_count",
			"last_error",
			"next_attempt_at",
			"updated_at",
		}),
	}).Create(&e)
	if res.Error != nil {
		return fmt.Errorf("upsert backfill_state for %s: %w", s.Token, res.Error)
	}
	return nil
}

func entityToState(e *entities.BackfillStateEntity) *BackfillState {
	if e == nil {
		return nil
	}
	return &BackfillState{
		Token:          e.Token,
		OldestTs:       cloneTimePtr(e.OldestTs),
		Disabled:       e.Disabled,
		DisabledReason: e.DisabledReason,
		ErrorCount:     e.ErrorCount,
		LastError:      e.LastError,
		NextAttemptAt:  cloneTimePtr(e.NextAttemptAt),
		UpdatedAt:      e.UpdatedAt,
	}
}

func stateToEntity(s *BackfillState) entities.BackfillStateEntity {
	return entities.BackfillStateEntity{
		Token:          s.Token,
		OldestTs:       cloneTimePtr(s.OldestTs),
		Disabled:       s.Disabled,
		DisabledReason: s.DisabledReason,
		ErrorCount:     s.ErrorCount,
		LastError:      s.LastError,
		NextAttemptAt:  cloneTimePtr(s.NextAttemptAt),
		UpdatedAt:      s.UpdatedAt,
	}
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}
