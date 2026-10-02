//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

type fakeLaunchpadIndexer struct {
	mu        sync.Mutex
	allowlist string
	launches  string
}

func (f *fakeLaunchpadIndexer) set(allowlist, launches string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowlist, f.launches = allowlist, launches
}

func (f *fakeLaunchpadIndexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.Contains(req.Query, "allowlistedTokensWithOrderbooks"):
		_, _ = fmt.Fprintf(w, `{"data":{"token":%s}}`, f.allowlist)
	case strings.Contains(req.Query, "GetLaunchesByTokens"):
		_, _ = fmt.Fprintf(w, `{"data":{"launchpad_launch":%s}}`, f.launches)
	default:
		http.Error(w, "unexpected query", http.StatusBadRequest)
	}
}

func allowlistedToken(addr, symbol string) string {
	return fmt.Sprintf(`{"address":%q,"token_id":0,"in_allowlist":true,"token_metadata":{"symbol":%q},"orderbooks":[]}`,
		addr, symbol)
}

func activeLaunch(addr string, rawPrice string) string {
	return fmt.Sprintf(`{"id":1,"name":"issuance","status":%d,"max_amount_cap":"1000","total_bought":"10",`+
		`"sale_start":"2026-01-01T00:00:00Z","sale_end":"2099-01-01T00:00:00Z","is_paused":false,`+
		`"updated_at":"2026-09-30T00:00:00Z","token":{"address":%q,"token_id":0},`+
		`"sale_options":[{"name":"Starter","total_bought":"10","max_amount_cap":"1000","is_paused":false,`+
		`"payments":[{"name":"USDT","price":%q,"token":{"address":"KT1USDT","token_id":0}}]}]}`,
		prices.LaunchStatusActive, addr, rawPrice)
}

// A contract redeploy leaves the retired token's row answering the same symbol
// as its successor; the sync must retire it without trusting a partial view.
func TestSyncRWALaunches_DisablesDelistedTokens(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.Upsert(ctx, prices.RWALaunch{
		Source: prices.SourceEquiteez, TokenAddr: "KT1OLD", LaunchID: 1,
		BaseSymbol: "xaug", QuoteSymbol: "usdt", Status: "active",
		Price: decimal.RequireFromString("125"), TotalBought: decimal.Zero,
		MaxAmountCap: decimal.RequireFromString("1000"),
	}, time.Now().UTC().Add(-24*time.Hour)))

	indexer := &fakeLaunchpadIndexer{}
	srv := httptest.NewServer(indexer)
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.Equiteez.IndexerURL = srv.URL
	cfg.API.TimeoutSeconds = 5

	indexer.set("["+allowlistedToken("KT1NEW", "XAUG")+"]", "["+activeLaunch("KT1NEW", "138000000")+"]")
	stored, err := jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	require.Equal(t, 1, stored)

	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "xaug", "usdt")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "KT1NEW", got.TokenAddr)
	require.True(t, got.Price.Equal(decimal.RequireFromString("138")), "price = %s", got.Price)
	list, err := repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 1, "the retired token must leave the catalog")

	// Still allowlisted but absent from the launches response: transient, keep it.
	indexer.set("["+allowlistedToken("KT1NEW", "XAUG")+"]", "[]")
	_, err = jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	list, err = repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 1)

	indexer.set("[]", "[]")
	_, err = jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	list, err = repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 1, "an empty allowlist (indexer mid-resync) must not retire the catalog")
}

func TestSyncRWALaunches_PreservesLaunchMissingFromPartialResponse(t *testing.T) {
	db := openGorm(t)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	ctx := context.Background()
	indexer := &fakeLaunchpadIndexer{}
	srv := httptest.NewServer(indexer)
	t.Cleanup(srv.Close)
	cfg := launchSyncTestConfig(srv.URL)
	allowlist := "[" + allowlistedToken("KT1AAA", "AAA") + "," + allowlistedToken("KT1BBB", "BBB") + "]"
	fullLaunches := "[" + activeLaunch("KT1AAA", "1000000") + "," + activeLaunch("KT1BBB", "2000000") + "]"

	indexer.set(allowlist, fullLaunches)
	stored, err := jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	require.Equal(t, 2, stored)

	// A successful refresh of BBB must not hide AAA when the complete
	// allowlist still contains it but the launch join temporarily omits it.
	indexer.set(allowlist, "["+activeLaunch("KT1BBB", "3000000")+"]")
	stored, err = jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	require.Equal(t, 1, stored)
	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "aaa", "usdt")
	require.NoError(t, err)
	require.True(t, found, "a missing launch join must preserve the allowlisted token")
	require.True(t, got.Price.Equal(decimal.NewFromInt(1)))
	list, err := repo.EnabledLaunches(ctx, prices.SourceEquiteez)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// An explicit unusable price has different semantics: retire AAA while
	// BBB confirms the response can refresh the catalog, then recover AAA.
	indexer.set(allowlist, "["+activeLaunch("KT1AAA", "0")+","+activeLaunch("KT1BBB", "3000000")+"]")
	stored, err = jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	require.Equal(t, 1, stored)
	_, found, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "aaa", "usdt")
	require.NoError(t, err)
	require.False(t, found, "an explicitly unusable launch must stop serving its old price")

	indexer.set(allowlist, fullLaunches)
	stored, err = jobs.SyncRWALaunches(ctx, cfg, repo, nil)
	require.NoError(t, err)
	require.Equal(t, 2, stored)
	_, found, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "aaa", "usdt")
	require.NoError(t, err)
	require.True(t, found, "a later usable launch must undo the sync disable")
}
