//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/entities"
	"quotes/internal/core/infrastructure/storage/repositories"
)

func TestEquiteezLaunchMembershipChangesWhileIndexerOffline(t *testing.T) {
	for _, contract := range []string{"base", "quote", "launchpad"} {
		t.Run(contract, func(t *testing.T) {
			db := openGorm(t)
			truncateRWA(t, db)
			truncateLaunches(t, db)
			require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied,backfill_state").Error)
			lookup := repositories.NewLookupRepository(db)
			launches := repositories.NewLaunchRepository(db)
			upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
			var delisted atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/allowlist" || !delisted.Load() {
					upstream.ServeHTTP(w, r)
					return
				}
				response := httptest.NewRecorder()
				upstream.ServeHTTP(response, r)
				var allowlist equiteez.ContractAllowlist
				if err := json.Unmarshal(response.Body.Bytes(), &allowlist); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				// Remove only the selected launch dependency; the other arrays
				// retain the fixture's authoritative memberships.
				switch contract {
				case "base":
					allowlist.BaseTokens = []string{recoveryBase}
				case "quote":
					allowlist.QuoteTokens = []string{}
				case "launchpad":
					allowlist.Launchpads = []string{}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(allowlist)
			}))
			t.Cleanup(srv.Close)
			cfg := recoveryBoundaryConfig(srv.URL)
			ctx := context.Background()
			require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
			original, found, err := launches.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, original.Enabled)
			require.True(t, original.InAllowlist)
			require.Equal(t, recoveryLaunchpad, original.LaunchpadAddr)
			require.Equal(t, recoveryQuote, original.QuoteAddr)
			var before entities.RWALaunchEntity
			require.NoError(t, db.Where("token_addr = ?", recoveryLaunchToken).Take(&before).Error)
			listed, err := launches.EnabledLaunches(ctx, prices.SourceEquiteez)
			require.NoError(t, err)
			require.Equal(t, []prices.RWALaunch{original}, listed)

			upstream.change(func() { upstream.offline = true })
			delisted.Store(true)
			require.ErrorContains(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil), "waiting for indexer")
			var hidden entities.RWALaunchEntity
			require.NoError(t, db.Where("token_addr = ?", recoveryLaunchToken).Take(&hidden).Error)
			require.True(t, hidden.Enabled, "CRM cannot change the manual enabled switch")
			require.False(t, hidden.InAllowlist)
			expected := before
			expected.InAllowlist = false
			require.Equal(t, expected, hidden, "delisting must preserve saved metadata and identity")
			listed, err = launches.EnabledLaunches(ctx, prices.SourceEquiteez)
			require.NoError(t, err)
			require.Empty(t, listed)
			_, found, err = launches.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
			require.NoError(t, err)
			require.False(t, found)

			// Keep the indexer offline: CRM alone must restore a known enabled
			// launch, without discovery or operator recovery configuration.
			delisted.Store(false)
			require.ErrorContains(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil), "waiting for indexer")
			var restored entities.RWALaunchEntity
			require.NoError(t, db.Where("token_addr = ?", recoveryLaunchToken).Take(&restored).Error)
			require.Equal(t, before, restored)
			listed, err = launches.EnabledLaunches(ctx, prices.SourceEquiteez)
			require.NoError(t, err)
			require.Equal(t, []prices.RWALaunch{original}, listed)
			launch, found, err := launches.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, original, launch)
		})
	}
}
