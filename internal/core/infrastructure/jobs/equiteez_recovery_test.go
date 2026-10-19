package jobs

import (
	"github.com/stretchr/testify/require"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"
	"testing"
	"time"
)

func TestBackfillReplaysOncePerGeneration(t *testing.T) {
	ts := time.Now().UTC()
	id := int64(500)
	st := &repositories.BackfillState{IndexerGeneration: "old", CursorTs: &ts, CursorID: &id, Disabled: true, DisabledReason: "auto_disabled", ErrorCount: 10}
	require.True(t, prepareEquiteezBackfill(st, "new"))
	require.Nil(t, st.CursorTs)
	require.Nil(t, st.CursorID)
	require.False(t, st.Disabled)
	st.CursorTs = &ts
	st.CursorID = &id
	require.False(t, prepareEquiteezBackfill(st, "new"))
	require.Equal(t, &id, st.CursorID)
	require.True(t, prepareEquiteezBackfill(st, "another"))
	require.Nil(t, st.CursorID)
}

func TestDelistedPairIsNotCollectedOrBackfilled(t *testing.T) {
	pairs := []prices.RWAPair{
		{ID: 1, Source: prices.SourceEquiteez, Enabled: true, InAllowlist: false},
		{ID: 2, Source: prices.SourceEquiteez, Enabled: false, InAllowlist: true},
		{ID: 3, Source: prices.SourceEquiteez, Enabled: true, InAllowlist: true},
	}
	require.Equal(t, []prices.RWAPair{pairs[2]}, filterEnabledPairs(pairs, prices.SourceEquiteez))
	require.Equal(t, []prices.RWAPair{pairs[2]}, filterBackfillablePairs(pairs))
}

func TestBackfillManualStopSurvivesGenerationChange(t *testing.T) {
	id := int64(17)
	st := &repositories.BackfillState{IndexerGeneration: "old", CursorID: &id, Disabled: true, DisabledReason: "manual"}
	require.False(t, prepareEquiteezBackfill(st, "new"))
	require.True(t, st.Disabled)
	require.Equal(t, "old", st.IndexerGeneration)
	require.Equal(t, &id, st.CursorID)
}
