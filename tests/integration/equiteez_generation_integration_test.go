//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/entities"
	"quotes/internal/core/infrastructure/storage/repositories"
)

type recoveryBoundaryRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// Wrappers inspect GraphQL without consuming the body needed by recoveryIndexer.
func readRecoveryBoundaryRequest(r *http.Request) (recoveryBoundaryRequest, error) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	var request recoveryBoundaryRequest
	if err != nil {
		return request, err
	}
	err = json.Unmarshal(body, &request)
	return request, err
}

func recoveryBoundaryConfig(endpoint string) *config.Config {
	cfg := &config.Config{}
	cfg.RWA.Enabled = true
	cfg.API.TimeoutSeconds = 10
	cfg.API.OutboundHTTPRetryMaxAttempts = 1
	cfg.Equiteez.IndexerURL = endpoint
	cfg.Equiteez.AllowlistURL = endpoint + "/allowlist"
	cfg.Equiteez.RequiredIndexes = []string{"token_index"}
	return cfg
}

func writeRecoveryBoundaryResponse(w http.ResponseWriter, response *httptest.ResponseRecorder) {
	for key, values := range response.Header() {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.Code)
	_, _ = w.Write(response.Body.Bytes())
}

func TestEquiteezCatalogRejectsMidreadGenerationChange(t *testing.T) {
	const originalGeneration = "2026-01-01T00:00:00Z"
	const rebuiltGeneration = "2026-10-19T00:00:00Z"
	for _, boundary := range []string{"catalogTokensWithOrderbooks", "GetLaunchesByTokens"} {
		t.Run(boundary, func(t *testing.T) {
			db := openGorm(t)
			truncateRWA(t, db)
			truncateLaunches(t, db)
			require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied,backfill_state").Error)
			lookup := repositories.NewLookupRepository(db)
			launches := repositories.NewLaunchRepository(db)
			upstream := &recoveryIndexer{generation: originalGeneration, status: "realtime", bookID: 7}
			var flip atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/allowlist" {
					upstream.ServeHTTP(w, r)
					return
				}
				request, err := readRecoveryBoundaryRequest(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				response := httptest.NewRecorder()
				upstream.ServeHTTP(response, r)
				// Flip before delivering the response, so the post-read status
				// check deterministically observes the replacement database.
				if strings.Contains(request.Query, boundary) && flip.Swap(false) {
					upstream.change(func() { upstream.generation = rebuiltGeneration })
				}
				writeRecoveryBoundaryResponse(w, response)
			}))
			t.Cleanup(srv.Close)
			cfg := recoveryBoundaryConfig(srv.URL)
			ctx := context.Background()
			require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
			pair, err := lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
			require.NoError(t, err)

			// Both selected recoveries and changed metadata must remain pending
			// until one complete, stable snapshot has been read.
			require.NoError(t, db.Exec("UPDATE rwa_pairs SET enabled=FALSE, base_symbol='saved-rec' WHERE id=?", pair.ID).Error)
			require.NoError(t, db.Exec("UPDATE rwa_launches SET enabled=FALSE, name='saved issuance', launch_id=99, price=125 WHERE token_addr=?", recoveryLaunchToken).Error)
			beforePair, err := lookup.LookupRWAPair(ctx, pair.ID)
			require.NoError(t, err)
			var beforeLaunch entities.RWALaunchEntity
			require.NoError(t, db.Where("token_addr = ?", recoveryLaunchToken).Take(&beforeLaunch).Error)
			cfg.Equiteez.RecoverOrderbooks = []string{recoveryBook}
			cfg.Equiteez.RecoverLaunchTokens = []string{recoveryLaunchToken}
			upstream.change(func() { upstream.bookID = 77 })
			flip.Store(true)

			err = jobs.SyncRWACatalog(ctx, cfg, lookup, nil)
			require.ErrorContains(t, err, "generation changed")
			require.False(t, flip.Load(), "the requested read boundary must have been reached")
			afterPair, err := lookup.LookupRWAPair(ctx, pair.ID)
			require.NoError(t, err)
			require.Equal(t, beforePair, afterPair, "a rejected snapshot cannot publish pair metadata or re-enable it")
			var afterLaunch entities.RWALaunchEntity
			require.NoError(t, db.Where("token_addr = ?", recoveryLaunchToken).Take(&afterLaunch).Error)
			require.Equal(t, beforeLaunch, afterLaunch, "a rejected snapshot cannot publish launch metadata or re-enable it")
			var recovered int64
			require.NoError(t, db.Table("equiteez_recovery_applied").Count(&recovered).Error)
			require.Zero(t, recovered, "failed discovery must not consume either one-time recovery")

			require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
			afterPair, err = lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
			require.NoError(t, err)
			require.Equal(t, pair.ID, afterPair.ID)
			require.True(t, afterPair.Enabled)
			require.EqualValues(t, 77, *afterPair.EquiteezOrderbookID)
			require.Equal(t, rebuiltGeneration, afterPair.IndexerGeneration)
			launch, found, err := launches.LaunchBySymbol(ctx, prices.SourceEquiteez, "primary", "usdt")
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, launch.Enabled)
			require.Equal(t, recoveryLaunchToken, launch.TokenAddr)
			require.Equal(t, 1, launch.LaunchID)
			require.Equal(t, "issuance", launch.Name)
			require.True(t, launch.Price.Equal(decimal.NewFromInt(138)))
			require.Equal(t, rebuiltGeneration, launch.IndexerGeneration)
			var recoveredKinds []string
			require.NoError(t, db.Table("equiteez_recovery_applied").Order("kind").Pluck("kind", &recoveredKinds).Error)
			require.Equal(t, []string{"launch", "orderbook"}, recoveredKinds)
		})
	}
}

func TestEquiteezBackfillRejectsMidreadGenerationChange(t *testing.T) {
	const originalGeneration = "2026-01-01T00:00:00Z"
	const rebuiltGeneration = "2026-10-19T00:00:00Z"
	for _, batch := range []string{"filled", "empty"} {
		t.Run(batch, func(t *testing.T) {
			db := openGorm(t)
			truncateRWA(t, db)
			truncateLaunches(t, db)
			require.NoError(t, db.Exec("TRUNCATE equiteez_allowlist,equiteez_recovery_applied,backfill_state").Error)
			lookup := repositories.NewLookupRepository(db)
			history := repositories.NewRWAPriceRepository(db)
			state := repositories.NewBackfillStateRepository(db)
			cursorAt := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
			filledAt := cursorAt.Add(time.Hour)
			upstream := &recoveryIndexer{generation: originalGeneration, status: "realtime", bookID: 7}
			var flip atomic.Bool
			requests := make(chan recoveryBoundaryRequest, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/allowlist" {
					upstream.ServeHTTP(w, r)
					return
				}
				request, err := readRecoveryBoundaryRequest(r)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if !strings.Contains(request.Query, "filledOrderbookOrders") {
					upstream.ServeHTTP(w, r)
					return
				}
				response := httptest.NewRecorder()
				upstream.ServeHTTP(response, r) // Retains the fixture's stale-book-ID check.
				if response.Code != http.StatusOK {
					writeRecoveryBoundaryResponse(w, response)
					return
				}
				changed := flip.Swap(false)
				if changed {
					upstream.change(func() { upstream.generation = rebuiltGeneration; upstream.bookID = 77 })
				}
				orders := []any{}
				if !changed || batch == "filled" {
					orders = append(orders, map[string]any{"id": 41, "order_type": 1, "price_per_rwa_token": "2000000", "fulfilled_amount": "1000000", "ended_at": filledAt.Format(time.RFC3339)})
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"orderbook_order": orders}})
				requests <- request
			}))
			t.Cleanup(srv.Close)
			cfg := recoveryBoundaryConfig(srv.URL)
			ctx := context.Background()
			require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
			pair, err := lookup.LookupRWAPairBySymbol(ctx, "rec", "usdt")
			require.NoError(t, err)
			tokens, err := lookup.Tokens(ctx)
			require.NoError(t, err)
			prices.RegisterTokens(tokens)
			cursorID := int64(40)
			require.NoError(t, state.Upsert(ctx, &repositories.BackfillState{Source: prices.SourceEquiteez, EntityKey: pair.EntityKey(), IndexerGeneration: originalGeneration, CursorID: &cursorID, CursorTs: &cursorAt}))
			beforeState, err := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
			require.NoError(t, err)
			require.NotNil(t, beforeState)
			_, err = history.Save(ctx, []prices.PricePoint{{Source: prices.SourceEquiteez, EntityKey: pair.EntityKey(), Metric: "last", Timestamp: cursorAt, Price: decimal.NewFromInt(3)}})
			require.NoError(t, err)
			cfg.Equiteez.Backfill.Enabled = true
			cfg.Equiteez.Backfill.TickSeconds = 3600

			// Wait for the actual fetch and then join the worker. Stop does not
			// cancel an in-flight batch, so assertions observe its final DB state.
			runBatch := func() recoveryBoundaryRequest {
				job := jobs.NewEquiteezBackfillJob(cfg, history, lookup, state, nil)
				t.Cleanup(job.Stop)
				job.Start(ctx)
				select {
				case request := <-requests:
					job.Stop()
					return request
				case <-time.After(15 * time.Second):
					t.Fatal("backfill never requested an order batch")
					return recoveryBoundaryRequest{}
				}
			}
			flip.Store(true)
			request := runBatch()
			require.False(t, flip.Load())
			require.Equal(t, float64(7), request.Variables["orderbook_id"])
			require.Equal(t, float64(cursorID), request.Variables["since_id"])
			require.Equal(t, cursorAt.Format(time.RFC3339Nano), request.Variables["since_ts"])
			afterState, err := state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
			require.NoError(t, err)
			require.Equal(t, beforeState, afterState, "discarded fills cannot advance the cursor; an empty crossed-generation batch cannot mark it caught up")
			var count int64
			require.NoError(t, db.Table("rwa_quote_prices").Where("pair_id = ?", pair.ID).Count(&count).Error)
			require.EqualValues(t, 1, count, "only the pre-existing history may remain")
			require.Error(t, lookup.CatalogGeneration(ctx, pair.ID, rebuiltGeneration))

			require.NoError(t, jobs.SyncRWACatalog(ctx, cfg, lookup, nil))
			require.NoError(t, lookup.CatalogGeneration(ctx, pair.ID, rebuiltGeneration))
			request = runBatch()
			require.Equal(t, float64(77), request.Variables["orderbook_id"])
			require.NotContains(t, request.Variables, "since_id", "the new generation must replay after catalog reconciliation")
			require.NotContains(t, request.Variables, "since_ts")
			afterState, err = state.Get(ctx, prices.SourceEquiteez, pair.EntityKey())
			require.NoError(t, err)
			require.NotNil(t, afterState)
			require.Equal(t, rebuiltGeneration, afterState.IndexerGeneration)
			require.NotNil(t, afterState.CursorID)
			require.EqualValues(t, 41, *afterState.CursorID)
			require.NotNil(t, afterState.CursorTs)
			require.True(t, filledAt.Equal(*afterState.CursorTs))
			require.False(t, afterState.Disabled)
			require.Zero(t, afterState.ErrorCount)
			require.Nil(t, afterState.NextAttemptAt)
			require.NoError(t, db.Table("rwa_quote_prices").Where("pair_id = ?", pair.ID).Count(&count).Error)
			require.EqualValues(t, 2, count, "healthy replay adds the rejected trade without deleting saved history")
			var point entities.RWAQuotePriceEntity
			require.NoError(t, db.Where("pair_id = ? AND side = 'last' AND ts = ?", pair.ID, filledAt).Take(&point).Error)
			require.True(t, point.Price.Equal(decimal.NewFromInt(2)))
		})
	}
}
