//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"quotes/internal/config"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/interactions/mvkt"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/repositories"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const valuationAsset = "KT1HR56nVuVDnidsabd2TBB9YvAtKbCbHKpP"
const valuationLaunch = "KT1U6KXwy8vduoq86HBjGp9m2Czc8rZM85MN"

func valuationFixture(at time.Time) valuation.Snapshot {
	id := valuation.SupplyIdentity{Network: "basenet", Contract: valuationAsset, TokenID: "0"}
	scale := 6
	price := decimal.NewFromInt(100)
	policy := valuation.Policy{}.Defaulted()
	q := policy.Quality(at, at, "")
	supply := valuation.Supply{Identity: id, Block: valuation.SupplyBlock{Level: 1000, Hash: "block1000", Timestamp: at.Add(-5 * time.Second)}, ObservedAt: at, Total: "50000000000", Excluded: "10000000000", Circulating: "40000000000", Metadata: valuation.Metadata{Decimals: &scale}, TotalQuality: q, CirculatingQuality: q, ScopeVersion: "v1", Source: "mvkt", Provenance: "observed"}
	p := valuation.Calculate(supply, valuation.Point{Symbol: "test-usdt", Quote: valuation.Quote{Contract: valuationLaunch, TokenID: "0", Symbol: "usdt"}, AsOf: at, PriceAt: at, PriceBasis: "last", Price: &price}, policy)
	eur := p.MarketCap.Mul(decimal.RequireFromString("0.92"))
	rate, fxAt := decimal.RequireFromString("0.92"), at.Add(-time.Second)
	p.Conversions = map[string]valuation.Conversion{"eur": {MarketCap: &eur, Rate: &rate, FXAt: &fxAt, MarketCapQuality: q, FDVQuality: q}}
	return valuation.Snapshot{Identity: id, Supply: supply, AsOf: at, Valuations: []valuation.Point{p}}
}

func TestValuationAtomicRevisionsAndHistoricalEvidence(t *testing.T) {
	db := openGorm(t)
	ctx := context.Background()
	repo := repositories.NewRWAValuationRepository(db)
	require.NoError(t, db.Exec("TRUNCATE rwa_valuation_current,rwa_supply_observations,rwa_valuation_points").Error)
	at := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	s := valuationFixture(at)
	first, err := repo.Save(ctx, s, 0, false)
	require.NoError(t, err)
	require.Equal(t, "1", first.Revision)
	retry, err := repo.Save(ctx, s, 0, false)
	require.NoError(t, err)
	require.Equal(t, "1", retry.Revision)
	newer := valuationFixture(at.Add(time.Minute))
	next, err := repo.Save(ctx, newer, 1, false)
	require.NoError(t, err)
	require.Equal(t, "2", next.Revision)
	delayed := valuationFixture(at.Add(30 * time.Second))
	_, err = repo.Save(ctx, delayed, 1, false)
	require.ErrorIs(t, err, repositories.ErrValuationSuperseded)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := valuationFixture(at.Add(time.Duration(2+i) * time.Minute))
			_, e := repo.Save(ctx, candidate, 2, false)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, repositories.ErrValuationSuperseded) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, conflict)
	current, ok, err := repo.Current(ctx, s.Identity)
	require.NoError(t, err)
	require.True(t, ok)
	reconstructed := current
	reconstructed.Supply.Provenance = "reconstructed"
	reconstructed.Supply.Total = "999"
	_, err = repo.Save(ctx, reconstructed, 3, true)
	require.NoError(t, err)
	unchanged, _, err := repo.Current(ctx, s.Identity)
	require.NoError(t, err)
	require.Equal(t, "50000000000", unchanged.Supply.Total)
	var total string
	require.NoError(t, db.Raw("SELECT total_supply::text FROM rwa_supply_observations WHERE observed_at=?", current.AsOf).Scan(&total).Error)
	require.Equal(t, "50000000000", total)
	// BIGINT revisions must never pass through float64.
	require.NoError(t, db.Exec("UPDATE rwa_valuation_current SET revision=9007199254740992").Error)
	correction := unchanged
	correction.Valuations = append([]valuation.Point(nil), unchanged.Valuations...)
	correctedRate := decimal.RequireFromString("0.93")
	conversion := correction.Valuations[0].Conversions["eur"]
	conversion.Rate = &correctedRate
	correctedCap := correction.Valuations[0].MarketCap.Mul(correctedRate)
	conversion.MarketCap = &correctedCap
	correction.Valuations[0].Conversions = map[string]valuation.Conversion{"eur": conversion}
	corrected, err := repo.Save(ctx, correction, 9007199254740992, false)
	require.NoError(t, err)
	require.Equal(t, "9007199254740993", corrected.Revision)
	body, err := json.Marshal(corrected)
	require.NoError(t, err)
	require.Contains(t, string(body), `"revision":"9007199254740993"`)
	invalid := corrected
	invalid.CurrentOnly = true
	invalid.Invalidated = true
	invalid.Reason = "chain_reorg"
	invalid.Supply.Block.Level = 999
	invalid, err = repo.Save(ctx, invalid, 9007199254740993, false)
	require.NoError(t, err)
	require.Equal(t, "9007199254740994", invalid.Revision)
	var validRows int64
	require.NoError(t, db.Table("rwa_valuation_points").Where("NOT invalidated").Count(&validRows).Error)
	require.Zero(t, validRows)
	replacement := valuationFixture(current.AsOf.Add(time.Minute))
	replacement.Supply.Block.Level = 998
	replacement.Supply.Block.Hash = "replacement"
	replacement, err = repo.Save(ctx, replacement, 9007199254740994, false)
	require.NoError(t, err)
	require.Equal(t, "9007199254740995", replacement.Revision)
}

func TestValuationAggregatesKeepClosingNullAndFXAsOf(t *testing.T) {
	db := openGorm(t)
	ctx := context.Background()
	repo := repositories.NewRWAValuationRepository(db)
	require.NoError(t, db.Exec("TRUNCATE rwa_valuation_current,rwa_supply_observations,rwa_valuation_points").Error)
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(-3 * 24 * time.Hour)
	s := valuationFixture(start.Add(57 * time.Minute))
	_, err := repo.Save(ctx, s, 0, false)
	require.NoError(t, err)
	require.NoError(t, repo.Refresh(ctx, start, start.Add(24*time.Hour)))
	for _, iv := range []app.Interval{app.Interval1m, app.Interval5m, app.Interval15m, app.Interval1h, app.Interval4h, app.Interval1d} {
		rows, coverage, err := repo.Series(ctx, s.Identity, "test-usdt", iv, start, start.Add(24*time.Hour), 100)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, s.AsOf, rows[0].Point.AsOf)
		require.Equal(t, "3680000", rows[0].Point.Conversions["eur"].MarketCap.String())
		require.Equal(t, s.Valuations[0].Conversions["eur"].FXAt, rows[0].Point.Conversions["eur"].FXAt)
		require.Equal(t, s.AsOf, *coverage.AvailableFrom)
	}
	missing := valuationFixture(start.Add(59 * time.Minute))
	missing.Valuations[0].MarketCap = nil
	missing.Valuations[0].MarketCapQuality = valuation.Missing("price_missing")
	_, err = repo.Save(ctx, missing, 1, false)
	require.NoError(t, err)
	require.NoError(t, repo.Refresh(ctx, start, start.Add(24*time.Hour)))
	rows, _, err := repo.Series(ctx, s.Identity, "test-usdt", app.Interval1h, start, start.Add(time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Point.MarketCap)
	require.Equal(t, "price_missing", rows[0].Point.MarketCapQuality.Reason)
	// A partial bucket must not read the closing observation from its future.
	rows, _, err = repo.Series(ctx, s.Identity, "test-usdt", app.Interval1h, start.Add(30*time.Minute), start.Add(58*time.Minute), 100)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, s.AsOf, rows[0].Point.AsOf)
	rows, _, err = repo.Series(ctx, s.Identity, "test-usdt", app.Interval1h, start, start.Add(50*time.Minute), 100)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestValuationScopeHistoryAndBackfillCheckpoints(t *testing.T) {
	db := openGorm(t)
	ctx := context.Background()
	lookup := repositories.NewLookupRepository(db)
	repo := repositories.NewRWAValuationRepository(db)
	url := "https://valuation.example/allowlist"
	require.NoError(t, db.Exec("DELETE FROM rwa_allowlist_versions WHERE url=?", url).Error)
	require.NoError(t, db.Exec("DELETE FROM equiteez_allowlist WHERE url=?", url).Error)
	doc := equiteez.ContractAllowlist{SchemaVersion: 1, BaseTokens: []string{valuationAsset}, QuoteTokens: []string{}, Orderbooks: []string{}, Launchpads: []string{valuationLaunch}}
	require.NoError(t, lookup.ApplyAllowlist(ctx, url, doc, true))
	a, err := lookup.AllowlistSnapshot(ctx, url, nil)
	require.NoError(t, err)
	require.Equal(t, doc.Launchpads, a.TreasuryLaunchpads)
	require.NoError(t, lookup.ApplyAllowlist(ctx, url, doc, true))
	same, err := lookup.AllowlistSnapshot(ctx, url, nil)
	require.NoError(t, err)
	require.Equal(t, a.Version, same.Version)
	doc.Launchpads = []string{}
	require.NoError(t, lookup.ApplyAllowlist(ctx, url, doc, true))
	removed, err := lookup.AllowlistSnapshot(ctx, url, nil)
	require.NoError(t, err)
	require.Equal(t, []string{valuationLaunch}, removed.TreasuryLaunchpads)
	require.Empty(t, removed.Document.Launchpads)
	require.NotEqual(t, a.Version, removed.Version)
	old, err := lookup.AllowlistSnapshot(ctx, url, &a.ValidFrom)
	require.NoError(t, err)
	require.Equal(t, a.Version, old.Version)
	before := a.ValidFrom.Add(-time.Second)
	_, err = lookup.AllowlistSnapshot(ctx, url, &before)
	require.ErrorIs(t, err, repositories.ErrAllowlistUnavailable)
	doc.Launchpads = []string{valuationLaunch}
	require.NoError(t, lookup.ApplyAllowlist(ctx, url, doc, true))
	back, err := lookup.AllowlistSnapshot(ctx, url, nil)
	require.NoError(t, err)
	require.NotEqual(t, a.Version, back.Version)
	// No CRM history at these old blocks: report durable gaps without fabricating supply.
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		level, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/blocks/"), 10, 64)
		_ = json.NewEncoder(w).Encode(valuation.SupplyBlock{Level: level, Hash: "block", Timestamp: start.Add(time.Duration(level) * time.Minute)})
	}))
	defer server.Close()
	cfg := &config.Config{MvKT: config.MvKTConfig{IndexerURL: server.URL}}
	scope := &equiteez.AllowlistProvider{Store: lookup, URL: url}
	job := jobs.NewRWASupplyJob(cfg, repo, lookup, repositories.NewLaunchRepository(db), scope, mvkt.NewClient(cfg.MvKT, &cfg.API), nil, nil, nil)
	req := jobs.ValuationBackfillRequest{JobID: "historical-gaps", Identity: valuation.SupplyIdentity{Network: cfg.RWA.PricingNetwork(), Contract: valuationAsset, TokenID: "0"}, FromLevel: 10, ToLevel: 12, Step: 1, MaxSteps: 2}
	report, err := job.Backfill(ctx, req)
	require.NoError(t, err)
	require.False(t, report.State.Complete)
	require.Equal(t, int64(12), report.State.NextLevel)
	require.Equal(t, int64(2), report.Coverage["allowlist_unavailable"])
	report, err = job.Backfill(ctx, req)
	require.NoError(t, err)
	require.True(t, report.State.Complete)
	require.Equal(t, int64(3), report.Coverage["allowlist_unavailable"])
	// Simulate a crash after checkpoint publication but before aggregate refresh.
	require.NoError(t, db.Exec("UPDATE rwa_valuation_backfill SET refreshed_through_level=10 WHERE job_id=?", req.JobID).Error)
	report, err = job.Backfill(ctx, req)
	require.NoError(t, err)
	require.Equal(t, int64(3), report.Coverage["allowlist_unavailable"])
	require.Equal(t, int64(12), report.State.RefreshedThroughLevel)
	req.Identity.TokenID = "1"
	_, err = job.Backfill(ctx, req)
	require.Error(t, err)
}
