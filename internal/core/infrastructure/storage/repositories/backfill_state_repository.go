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
//
// CursorTs + CursorID are the keyset cursor for forward-walking sources:
// Equiteez resumes filled orders strictly after (ended_at, id), i.e. by FILL
// time with the creation-time id only as a tie-break. CursorTs == nil means the
// walk restarts from start_from. CoinGecko backfill (a backward walk bounded by
// OldestTs) leaves both nil.
type BackfillState struct {
	Source         prices.Source
	EntityKey      string
	OldestTs       *time.Time
	CursorID       *int64
	CursorTs       *time.Time
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
	// BackfillDisabledReasonCaughtUp is LEGACY: earlier builds used it to
	// permanently disable a forward-walking backfill once it reached the latest
	// upstream event. That was wrong — a forward walk has no terminal state
	// (new fills keep arriving), so catching up is a pause, not completion.
	// Jobs now set NextAttemptAt instead; the constant remains so
	// ClearCaughtUp can resume rows written by older builds.
	BackfillDisabledReasonCaughtUp = "caught_up"
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

// ClearCaughtUp re-enables every row for `source` that a previous build parked
// with disabled_reason='caught_up', returning how many were resumed.
//
// `cursor_id` is deliberately left intact so the walk resumes exactly where it
// stopped instead of replaying history. Rows disabled for a genuinely terminal
// or operator-owned reason (reached_floor, manual) are untouched; legacy
// auto_disabled rows are resumed separately by ClearAutoDisabled.
//
// Called at job start so a deploy self-heals pairs frozen by the old sticky
// behaviour — no ops SQL required.
func (r *BackfillStateRepository) ClearCaughtUp(ctx context.Context, source prices.Source) (int64, error) {
	if source == "" {
		return 0, fmt.Errorf("source is required")
	}
	res := r.db.WithContext(ctx).
		Model(&entities.BackfillStateEntity{}).
		Where("source_code = ? AND disabled = ? AND disabled_reason = ?",
			string(source), true, BackfillDisabledReasonCaughtUp).
		Updates(map[string]any{
			"disabled":        false,
			"disabled_reason": "",
			"next_attempt_at": nil,
			"error_count":     0,
			"last_error":      "",
			"updated_at":      time.Now().UTC(),
		})
	if res.Error != nil {
		return 0, fmt.Errorf("clear caught_up backfill_state: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ClearAutoDisabled re-enables every row for `source` that repeated errors
// parked with disabled_reason='auto_disabled', returning how many were resumed.
// Only older builds wrote that reason (current ones convert the error threshold
// into a cooldown); without this a past provider outage keeps backfill dead
// across deploys. Operator/terminal disables (manual, reached_floor) survive.
func (r *BackfillStateRepository) ClearAutoDisabled(ctx context.Context, source prices.Source) (int64, error) {
	if source == "" {
		return 0, fmt.Errorf("source is required")
	}
	res := r.db.WithContext(ctx).
		Model(&entities.BackfillStateEntity{}).
		Where("source_code = ? AND disabled = ? AND disabled_reason = ?",
			string(source), true, BackfillDisabledReasonAutoDisabled).
		Updates(map[string]any{
			"disabled":        false,
			"disabled_reason": "",
			"next_attempt_at": nil,
			"error_count":     0,
			"last_error":      "",
			"updated_at":      time.Now().UTC(),
		})
	if res.Error != nil {
		return 0, fmt.Errorf("clear auto_disabled backfill_state: %w", res.Error)
	}
	return res.RowsAffected, nil
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
			"cursor_id",
			"cursor_ts",
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
		CursorID:       cloneInt64Ptr(e.CursorID),
		CursorTs:       cloneTimePtr(e.CursorTs),
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
		CursorID:       cloneInt64Ptr(s.CursorID),
		CursorTs:       cloneTimePtr(s.CursorTs),
		Disabled:       s.Disabled,
		DisabledReason: s.DisabledReason,
		ErrorCount:     s.ErrorCount,
		LastError:      s.LastError,
		NextAttemptAt:  cloneTimePtr(s.NextAttemptAt),
		UpdatedAt:      s.UpdatedAt,
	}
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}
