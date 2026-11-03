package primary

import (
	"context"
	"github.com/stretchr/testify/require"
	app "quotes/internal/core/application/prices"
	"testing"
	"time"
)

func TestListingPriceIgnoresSaleEligibilityOnly(t *testing.T) {
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	for _, state := range []string{"ACTIVE", "INACTIVE", "CLOSED", "PAUSED", "OPTION_PAUSED", "OUTSIDE_WINDOW"} {
		t.Run(state, func(t *testing.T) {
			r := record("100000000", testPayment)
			switch state {
			case "PAUSED":
				r.IsPaused = true
			case "OPTION_PAUSED":
				o := r.Options["Starter"]
				o.IsPaused = true
				r.Options["Starter"] = o
			case "OUTSIDE_WINDOW":
				end := at.Add(-time.Hour)
				r.SaleEnd = &end
			default:
				r.Status = state
			}
			history := launchHistory{launchpad: "launchpad", key: "test", payments: knownPayments(testPayment), updates: []update{{ID: 1, Level: 1, Timestamp: at.Add(-2 * time.Hour), Action: "add_key", Value: r}}}
			h := &History{allowlist: fixedAllowlist{Version: "v1"}, cacheVersion: "v1", expires: time.Now().Add(time.Hour), snapshot: &snapshot{head: head{Level: 100, Timestamp: at}, histories: []launchHistory{history}}}
			req := app.PrimaryRequest{Network: "basenet", Contract: testAssetContract, TokenID: "0", Level: 100, At: at, Mode: app.PrimaryHoldings}
			got, err := h.ListingTerms(context.Background(), req)
			require.NoError(t, err)
			require.Equal(t, "100", got.Price)
			_, err = h.Terms(context.Background(), req)
			if state == "ACTIVE" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestSharedScopeDoesNotHaveFourLaunchpadLimit(t *testing.T) {
	h := &History{allowlist: fixedAllowlist{Version: "five", TreasuryLaunchpads: []string{"one", "two", "three", "four", "five"}}}
	addresses, err := h.launchpads(context.Background())
	require.NoError(t, err)
	require.Len(t, addresses, 5)
	h.allowlist = fixedAllowlist{TreasuryLaunchpads: make([]string, historyRowLimit+1)}
	_, err = h.launchpads(context.Background())
	require.Error(t, err)
}
