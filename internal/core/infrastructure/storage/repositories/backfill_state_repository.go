package repositories

import (
	"context"
	"errors"
	"fmt"
	"time"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/entities"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BackfillState is the domain-facing view of one (source, entity_key) cursor.
type BackfillState struct {
	Source         prices.Source
	EntityKey      string
	OldestTs       *time.Time
	Disabled       bool
	DisabledReason string
	ErrorCount     int
	LastError      string
	NextAttemptAt  *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Reason constants for BackfillState.DisabledReason. Kept here so jobs and metrics
// labels never disagree.
const (
	BackfillDisabledReasonReachedFloor = "reached_floor"
	BackfillDisabledReasonAutoDisabled = "auto_disabled"
	BackfillDisabledReasonManual       = "manual"
)

type BackfillStateRepository struct {
	db *gorm.DB
}

func NewBackfillStateRepository(db *gorm.DB) *BackfillStateRepository {
	return &BackfillStateRepository{db: db}
}

// Get returns the row for (source, entity_key) or (nil, nil) when missing.
func (r *BackfillStateRepository) Get(ctx context.Context, source prices.Source, entityKey string) (*BackfillState, error) {
	if source == "" || entityKey == "" {
		return nil, fmt.Errorf("source and entity_key are required")
	}
	var e entities.BackfillStateEntity
	res := r.db.WithContext(ctx).
		Where("source_code = ? AND entity_key = ?", string(source), entityKey).
		Take(&e)
	if res.Error != nil {
		if errors.Is(res.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get backfill_state: %w", res.Error)
	}
	return entityToState(&e), nil
}

// Upsert writes the current state, creating the row on first call. updated_at is
// always stamped to NOW(UTC).
//
// Manual re-enable semantics (refactoring_v2 §2.2): if the row currently has
// disabled=true and the incoming state has disabled=false, ErrorCount/LastError/
// NextAttemptAt are wiped before persistence. This stops a token that an
// operator just unblocked from immediately tripping the auto-disable threshold
// on the next failure.
func (r *BackfillStateRepository) Upsert(ctx context.Context, s *BackfillState) error {
	if s == nil {
		return fmt.Errorf("state is required")
	}
	if s.Source == "" || s.EntityKey == "" {
		return fmt.Errorf("state.source and state.entity_key are required")
	}

	prev, err := r.Get(ctx, s.Source, s.EntityKey)
	if err != nil {
		return fmt.Errorf("read existing backfill_state: %w", err)
	}
	if prev != nil && prev.Disabled && !s.Disabled {
		s.ErrorCount = 0
		s.LastError = ""
		s.NextAttemptAt = nil
		s.DisabledReason = ""
	}

	e := stateToEntity(s)
	e.UpdatedAt = time.Now().UTC()
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_code"}, {Name: "entity_key"}},
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
		return fmt.Errorf("upsert backfill_state: %w", res.Error)
	}
	return nil
}

func entityToState(e *entities.BackfillStateEntity) *BackfillState {
	if e == nil {
		return nil
	}
	return &BackfillState{
		Source:         prices.Source(e.SourceCode),
		EntityKey:      e.EntityKey,
		OldestTs:       cloneTimePtr(e.OldestTs),
		Disabled:       e.Disabled,
		DisabledReason: e.DisabledReason,
		ErrorCount:     e.ErrorCount,
		LastError:      e.LastError,
		NextAttemptAt:  cloneTimePtr(e.NextAttemptAt),
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
	}
}

func stateToEntity(s *BackfillState) entities.BackfillStateEntity {
	return entities.BackfillStateEntity{
		SourceCode:     string(s.Source),
		EntityKey:      s.EntityKey,
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
