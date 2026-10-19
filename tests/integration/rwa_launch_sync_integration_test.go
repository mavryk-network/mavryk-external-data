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
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/repositories"
)

var recoveryBase = "KT1" + strings.Repeat("A", 33)
var recoveryQuote = "KT1" + strings.Repeat("B", 33)
var recoveryBook = "KT1" + strings.Repeat("C", 33)
var recoveryLaunchToken = "KT1" + strings.Repeat("D", 33)
var recoveryLaunchpad = "KT1" + strings.Repeat("E", 33)

type recoveryIndexer struct {
	statusRequests, liveRequests        int
	flipOnLive                          bool
	launchMissing                       bool
	mu                                  sync.Mutex
	generation                          string
	status                              string
	partial, offline, crmDown, delisted bool
	bookID                              int
	orderErrors                         int
	orderRequests                       int
}

func (f *recoveryIndexer) change(update func()) { f.mu.Lock(); defer f.mu.Unlock(); update() }
func (f *recoveryIndexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/allowlist" {
		if f.crmDown {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		bases := []string{recoveryBase, recoveryLaunchToken}
		if f.delisted {
			bases = []string{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "base_tokens": bases, "quote_tokens": []string{recoveryQuote}, "orderbooks": []string{recoveryBook}, "launchpads": []string{recoveryLaunchpad}})
		return
	}
	if f.offline {
		http.Error(w, "reindexing", http.StatusServiceUnavailable)
		return
	}
	var request struct {
		Query     string
		Variables map[string]any
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	var result any
	switch {
	case strings.Contains(request.Query, "quotesIndexerStatus"):
		f.statusRequests++
		result = map[string]any{"dipdup_schema": []any{map[string]any{"name": "public", "created_at": f.generation}}, "dipdup_head": []any{map[string]any{"timestamp": time.Now().UTC(), "updated_at": time.Now().UTC()}}, "dipdup_index": []any{map[string]any{"name": "token_index", "status": f.status}}}
	case strings.Contains(request.Query, "catalogTokensWithOrderbooks"), strings.Contains(request.Query, "tokensWithOrderbooks"):
		tokens := []any{}
		if !f.partial {
			tokens = append(tokens, map[string]any{"address": recoveryBase, "token_id": 0, "in_allowlist": false, "token_metadata": map[string]string{"symbol": "REC"}, "orderbooks": []any{map[string]any{"id": f.bookID, "address": recoveryBook, "in_allowlist": false, "last_matched_price": "2000000", "currencies": []any{map[string]any{"currency_name": "USDT", "token": map[string]any{"address": recoveryQuote, "token_id": 0}}}}}})
			tokens = append(tokens, map[string]any{"address": recoveryLaunchToken, "token_id": 0, "token_metadata": map[string]string{"symbol": "PRIMARY"}, "orderbooks": []any{}})
		}
		if strings.Contains(request.Query, "query tokensWithOrderbooks") {
			f.liveRequests++
			if f.flipOnLive {
				f.flipOnLive = false
				f.generation = "2026-10-19T01:00:00Z"
			}
		}
		result = map[string]any{"token": tokens}
	case strings.Contains(request.Query, "GetLaunchesByTokens"):
		launches := []any{}
		if !f.partial && !f.launchMissing {
			var launch any
			body := fmt.Sprintf(`{"id":1,"name":"issuance","status":%d,"max_amount_cap":"1000","total_bought":"10","sale_start":"2026-01-01T00:00:00Z","sale_end":"2099-01-01T00:00:00Z","updated_at":"2026-09-30T00:00:00Z","token":{"address":%q,"token_id":0},"launchpad":{"address":%q},"sale_options":[{"name":"Starter","payments":[{"name":"USDT","price":"138000000","token":{"address":%q,"token_id":0}}]}]}`, prices.LaunchStatusActive, recoveryLaunchToken, recoveryLaunchpad, recoveryQuote)
			_ = json.Unmarshal([]byte(body), &launch)
			launches = append(launches, launch)
		}
		result = map[string]any{"launchpad_launch": launches}
	case strings.Contains(request.Query, "filledOrderbookOrders"):
		f.orderRequests++
		if f.orderErrors > 0 {
			f.orderErrors--
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		if request.Variables["orderbook_id"] != float64(f.bookID) {
			http.Error(w, "stale book ID", 400)
			return
		}
		orders := []any{}
		if _, resuming := request.Variables["since_id"]; !resuming {
			orders = append(orders, map[string]any{"id": 1, "order_type": 1, "price_per_rwa_token": "2000000", "fulfilled_amount": "1000000", "ended_at": time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)})
		}
		result = map[string]any{"orderbook_order": orders}
	default:
		http.Error(w, "unexpected query", 400)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
}

func TestEquiteezCatalogSurvivesReindexAndCRMOutage(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied,backfill_state").Error)
	lookup := repositories.NewLookupRepository(db)
	launches := repositories.NewLaunchRepository(db)
	upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.API.TimeoutSeconds = 5
	cfg.API.OutboundHTTPRetryMaxAttempts = 1
	cfg.Equiteez.IndexerURL = srv.URL
	cfg.Equiteez.AllowlistURL = srv.URL + "/allowlist"
	cfg.Equiteez.RequiredIndexes = []string{"token_index"}
	ctx := context.Background()
	syncCatalog := func() error { return jobs.SyncRWACatalog(ctx, cfg, lookup, nil) }
	require.NoError(t, syncCatalog())
	pair, err := lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
	require.NoError(t, err)
	originalID := pair.ID
	_, found, err := launches.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
	require.NoError(t, err)
	require.True(t, found)
	history := repositories.NewRWAPriceRepository(db)
	_, err = history.Save(ctx, []prices.PricePoint{{Source: prices.SourceEquiteez, EntityKey: pair.EntityKey(), Metric: "last", Timestamp: time.Now().UTC().Add(-24 * time.Hour), Price: decimal.NewFromInt(3)}})
	require.NoError(t, err)
	upstream.change(func() { upstream.offline = true })
	require.Error(t, syncCatalog())
	upstream.change(func() {
		upstream.offline = false
		upstream.partial = true
		upstream.generation = "2026-10-19T00:00:00Z"
		upstream.status = "syncing"
	})
	require.Error(t, syncCatalog())
	pair, err = lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
	require.NoError(t, err)
	require.Equal(t, originalID, pair.ID)
	require.Error(t, lookup.CatalogGeneration(ctx, pair.ID, upstream.generation))
	// Even an empty successful response must never remove saved catalog entries.
	upstream.change(func() { upstream.status = "realtime" })
	require.NoError(t, syncCatalog())
	listed, err := lookup.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	// Simulate the legacy automatic disable and an unrelated manual launch disable.
	require.NoError(t, db.Exec("UPDATE rwa_pairs SET enabled=FALSE WHERE id=?", pair.ID).Error)
	require.NoError(t, db.Exec("UPDATE rwa_launches SET enabled=FALSE").Error)
	cfg.Equiteez.RecoverOrderbooks = []string{recoveryBook}
	upstream.change(func() { upstream.partial = false; upstream.bookID = 77; upstream.crmDown = true })
	// A new repository instance proves the fallback survives a process restart.
	lookup = repositories.NewLookupRepository(db)
	require.NoError(t, syncCatalog())
	pair, err = lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
	require.NoError(t, err)
	require.Equal(t, originalID, pair.ID)
	require.EqualValues(t, 77, *pair.EquiteezOrderbookID)
	require.Equal(t, "2026-10-19T00:00:00Z", pair.IndexerGeneration)
	_, found, err = launches.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
	require.NoError(t, err)
	require.False(t, found, "manual launch disable survives")
	var count int64
	require.NoError(t, db.Table("rwa_quote_prices").Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Exec("UPDATE rwa_pairs SET enabled=FALSE WHERE id=?", pair.ID).Error)
	require.NoError(t, syncCatalog())
	hidden, err := lookup.LookupRWAPair(ctx, pair.ID)
	require.NoError(t, err)
	require.False(t, hidden.Enabled, "recovery must run only once")
	require.NoError(t, db.Exec("UPDATE rwa_pairs SET enabled=TRUE WHERE id=?", pair.ID).Error)
	upstream.change(func() { upstream.crmDown = false; upstream.delisted = true })
	require.NoError(t, syncCatalog())
	listed, err = lookup.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Empty(t, listed)
	upstream.change(func() { upstream.delisted = false })
	require.NoError(t, syncCatalog())
	listed, err = lookup.EnabledRWAPairs(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	// An old automatically disabled history cursor must replay under the new generation.
	state := repositories.NewBackfillStateRepository(db)
	oldID := int64(999)
	oldTime := time.Now().UTC()
	require.NoError(t, state.Upsert(ctx, &repositories.BackfillState{Source: prices.SourceEquiteez, EntityKey: pair.EntityKey(), IndexerGeneration: "old", CursorID: &oldID, CursorTs: &oldTime, Disabled: true, DisabledReason: "auto_disabled"}))
	tokens, err := lookup.Tokens(ctx)
	require.NoError(t, err)
	prices.RegisterTokens(tokens)
	cfg.Equiteez.Backfill.Enabled = true
	cfg.Equiteez.Backfill.TickSeconds = 1
	cfg.Equiteez.Backfill.BackfillMaxErrors = 1
	cfg.Equiteez.Backfill.BackoffInitialMs = 1000
	cfg.Equiteez.Backfill.BackoffMaxMs = 1000
	upstream.change(func() { upstream.orderErrors = 2 })
	job := jobs.NewEquiteezBackfillJob(cfg, history, lookup, state, nil)
	job.Start(ctx)
	defer job.Stop()
	t.Cleanup(func() {
		if t.Failed() {
			st, e := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
			t.Logf("backfill state: %+v, error: %v", st, e)
		}
	})
	require.Eventually(t, func() bool {
		st, e := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
		return e == nil && st != nil && !st.Disabled && st.CursorID != nil && *st.CursorID == 1 && st.IndexerGeneration == pair.IndexerGeneration
	}, 20*time.Second, 100*time.Millisecond)
	require.NoError(t, db.Table("rwa_quote_prices").Count(&count).Error)
	require.EqualValues(t, 2, count, "old history is preserved and replay adds the missing trade")
	job.Stop()
	var candles int64
	require.NoError(t, db.Table("rwa_quote_prices_1h").Where("pair_id = ?", pair.ID).Count(&candles).Error)
	require.Positive(t, candles, "replayed trades must be visible to charts")
	restarted := jobs.NewEquiteezBackfillJob(cfg, history, lookup, state, nil)
	restarted.Start(ctx)
	require.Eventually(t, func() bool {
		st, e := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
		return e == nil && st.NextAttemptAt != nil
	}, 5*time.Second, 50*time.Millisecond)
	restarted.Stop()
	require.NoError(t, db.Table("rwa_quote_prices").Count(&count).Error)
	require.EqualValues(t, 2, count, "restart must not replay or duplicate history")

}

func TestEquiteezLiveDoesNotWriteDuringReindexOrGenerationChange(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied,backfill_state").Error)
	lookup := repositories.NewLookupRepository(db)
	upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
	srv := httptest.NewServer(upstream)
	defer srv.Close()
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.RWA.IntervalSeconds = 1
	cfg.API.TimeoutSeconds = 5
	cfg.Equiteez.IndexerURL = srv.URL
	cfg.Equiteez.AllowlistURL = srv.URL + "/allowlist"
	cfg.Equiteez.RequiredIndexes = []string{"token_index"}
	ctx := context.Background()
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	tokens, err := lookup.Tokens(ctx)
	require.NoError(t, err)
	prices.RegisterTokens(tokens)
	history := repositories.NewRWAPriceRepository(db)
	upstream.change(func() { upstream.status = "syncing" })
	live := jobs.NewEquiteezRWAJob(cfg, history, lookup, nil)
	live.Start(ctx)
	defer live.Stop()
	require.Eventually(t, func() bool { upstream.mu.Lock(); defer upstream.mu.Unlock(); return upstream.statusRequests >= 3 }, 5*time.Second, 50*time.Millisecond)
	var count int64
	require.NoError(t, db.Table("rwa_quote_prices").Count(&count).Error)
	require.Zero(t, count)
	upstream.change(func() { upstream.status = "realtime"; upstream.flipOnLive = true })
	require.Eventually(t, func() bool { upstream.mu.Lock(); defer upstream.mu.Unlock(); return upstream.liveRequests > 0 }, 5*time.Second, 50*time.Millisecond)
	live.Stop()
	require.NoError(t, db.Table("rwa_quote_prices").Count(&count).Error)
	require.Zero(t, count, "snapshot crossing a generation boundary must be discarded")
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	resumed := jobs.NewEquiteezRWAJob(cfg, history, lookup, nil)
	resumed.Start(ctx)
	defer resumed.Stop()
	require.Eventually(t, func() bool { var n int64; return db.Table("rwa_quote_prices").Count(&n).Error == nil && n > 0 }, 5*time.Second, 50*time.Millisecond)
}

// A stalled CRM must leave time to load the persisted snapshot and reconcile a
// rebuilt indexer; sharing the whole tick deadline used to cancel the fallback.
func TestEquiteezCatalogUsesCacheWhenCRMStalls(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied").Error)
	lookup := repositories.NewLookupRepository(db)
	upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
	var stallCRM atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/allowlist" && stallCRM.Load() {
			<-r.Context().Done()
			return
		}
		upstream.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.API.TimeoutSeconds = 5
	cfg.API.OutboundHTTPRetryMaxAttempts = 1
	cfg.Equiteez.IndexerURL = srv.URL
	cfg.Equiteez.AllowlistURL = srv.URL + "/allowlist"
	cfg.Equiteez.RequiredIndexes = []string{"token_index"}
	require.NoError(t, jobs.SyncRWACatalog(context.Background(), cfg, lookup, nil))
	upstream.change(func() { upstream.generation = "2026-10-19T00:00:00Z"; upstream.bookID = 77 })
	stallCRM.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	pair, err := lookup.LookupRWAPairBySymbol(context.Background(), "rec", "usdt")
	require.NoError(t, err)
	require.EqualValues(t, 77, *pair.EquiteezOrderbookID)
	require.Equal(t, "2026-10-19T00:00:00Z", pair.IndexerGeneration)
}

func TestSyncRWACatalog_PreservesLaunchMissingFromPartialResponse(t *testing.T) {
	db := openGorm(t)
	truncateRWA(t, db)
	truncateLaunches(t, db)
	repo := repositories.NewLaunchRepository(db)
	lookup := repositories.NewLookupRepository(db)
	ctx := context.Background()
	upstream := &recoveryIndexer{generation: "2026-01-01T00:00:00Z", status: "realtime", bookID: 7}
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	cfg := launchSyncTestConfig(srv.URL)
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))

	// A pair refresh succeeds while the still-allowlisted launch join is absent.
	upstream.change(func() { upstream.launchMissing = true; upstream.bookID = 77 })
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	got, found, err := repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
	require.NoError(t, err)
	require.True(t, found, "a missing launch join must preserve the allowlisted token")
	require.True(t, got.Price.Equal(decimal.NewFromInt(138)))
	pair, err := lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
	require.NoError(t, err)
	require.EqualValues(t, 77, *pair.EquiteezOrderbookID)

	upstream.change(func() { upstream.delisted = true })
	require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
	_, found, err = repo.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
	require.NoError(t, err)
	require.False(t, found, "CRM removal is authoritative even if the launch join is absent")
}
