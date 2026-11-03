//go:build integration

package integration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"quotes/internal/config"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/interactions/mvkt"
	"quotes/internal/core/infrastructure/jobs"
	"quotes/internal/core/infrastructure/storage/repositories"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type valuationFXFixture struct{ at time.Time }

func (f *valuationFXFixture) Convert(_ context.Context, _ prices.Token, _ prices.Currency, amount decimal.Decimal, at time.Time) (app.ConversionResult, error) {
	f.at = at
	rate := decimal.RequireFromString("0.92")
	return app.ConversionResult{Amount: amount.Mul(rate), Rate: rate, RateTS: at.Add(-time.Minute)}, nil
}

func TestValuationCollectorSecondarySupplyAndRecovery(t *testing.T) {
	db := openGorm(t)
	ctx := context.Background()
	truncatePairs(t, db)
	require.NoError(t, db.Exec("TRUNCATE rwa_valuation_current,rwa_supply_observations,rwa_valuation_points,rwa_valuation_attempts").Error)
	lookup := repositories.NewLookupRepository(db)
	store := repositories.NewRWAValuationRepository(db)
	quote := "KT1Pn5Zpx1bJx5H51btk92pfwvUMCKtp2Q2v"
	book := "KT1UqpQV34HZ2iGBxHtPKtdriwenu83Zy7mS"
	doc := equiteez.ContractAllowlist{SchemaVersion: 1, BaseTokens: []string{valuationAsset}, QuoteTokens: []string{quote}, Orderbooks: []string{book}, Launchpads: []string{valuationLaunch}}
	scopeURL := "https://collector.example/allowlist"
	require.NoError(t, lookup.ApplyAllowlist(ctx, scopeURL, doc, true))
	id, err := lookup.UpsertRWAPair(ctx, prices.RWAPair{Source: prices.SourceEquiteez, BaseSymbol: "test", QuoteSymbol: "usdt", TokenAddr: valuationAsset, QuoteAddr: quote, OrderbookAddr: book, InAllowlist: true, IndexerGeneration: "g"}, time.Now())
	require.NoError(t, err)
	marketRepo := repositories.NewRWAMarketSnapshotRepository(db)
	zero := 0
	price := decimal.NewFromInt(100)
	market := prices.RWAMarketSnapshot{PairID: id, Network: "basenet", IndexerGeneration: "g", ObservedAt: time.Now().UTC().Add(-time.Second), TokenAddr: valuationAsset, TokenID: &zero, QuoteAddr: quote, QuoteTokenID: &zero, OrderbookAddr: book, NativeQuote: "usdt", LastPrice: &price}
	require.NoError(t, marketRepo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{market}))
	var scale atomic.Int64
	scale.Store(6)
	var height atomic.Int64
	height.Store(1002)
	var balance atomic.Int64
	balance.Store(10000000000)
	var fail atomic.Bool
	var hash atomic.Value
	hash.Store("block1000")
	blockTime := time.Now().UTC().Add(-10 * time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		var body any
		level := height.Load() - 2
		path := strings.ReplaceAll(r.URL.Path, "/"+strconv.FormatInt(level, 10), "/1000")
		switch path {
		case "/head":
			body = mvkt.SupplyHead{SupplyBlock: valuation.SupplyBlock{Level: height.Load(), Hash: "head", Timestamp: blockTime.Add(5 * time.Second)}, ChainID: "NetXmtMsNf69w1w", KnownLevel: height.Load(), LastSync: time.Now().UTC(), Synced: true}
		case "/blocks/1000":
			body = valuation.SupplyBlock{Level: level, Hash: hash.Load().(string), Timestamp: blockTime}
		case "/tokens":
			body = []any{map[string]any{"contract": map[string]string{"address": valuationAsset}, "tokenId": "0", "standard": "fa2", "firstLevel": 1}}
		case "/contracts/" + valuationAsset + "/storage":
			body = map[string]int{"total_supply": 42, "token_metadata": 43}
			require.Equal(t, strconv.FormatInt(level, 10), r.URL.Query().Get("level"))
		case "/contracts/" + valuationLaunch + "/storage":
			body = map[string]int{"launchLedger": 50, "treasuryLedger": 51}
		case "/bigmaps/42/historical_keys/1000":
			body = []any{map[string]any{"id": 1, "active": true, "key": "0", "value": "50000000000"}}
		case "/bigmaps/43/historical_keys/1000":
			body = []any{map[string]any{"id": 1, "active": true, "key": "0", "value": map[string]any{"token_id": "0", "token_info": map[string]string{"decimals": hex.EncodeToString([]byte(strconv.FormatInt(scale.Load(), 10))), "symbol": hex.EncodeToString([]byte("TEST"))}}}}
		case "/bigmaps/50/historical_keys/1000":
			body = []any{map[string]any{"id": 1, "active": true, "key": "launch1", "value": map[string]string{"tokenContractAddress": valuationAsset, "tokenId": "0", "status": "CLOSED"}}}
		case "/bigmaps/51/historical_keys/1000":
			body = []any{map[string]any{"id": 1, "active": true, "key": "launchTreasury", "value": "treasury"}}
		case "/tokens/historical_balances/1000":
			body = []any{map[string]any{"account": map[string]string{"address": "treasury"}, "token": map[string]any{"contract": map[string]string{"address": valuationAsset}, "tokenId": "0", "standard": "fa2"}, "balance": fmt.Sprint(balance.Load())}}
		default:
			t.Errorf("unexpected collector request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	cfg := &config.Config{MvKT: config.MvKTConfig{IndexerURL: server.URL}, API: config.APIConfig{OutboundHTTPCircuitBreakerDisabled: true, OutboundHTTPRetryMaxAttempts: 1}, Server: config.ServerConfig{FXMaxStalenessSeconds: 3600}}
	scope := &equiteez.AllowlistProvider{Store: lookup, URL: scopeURL}
	fx := &valuationFXFixture{}
	collector := jobs.NewRWASupplyJob(cfg, store, lookup, repositories.NewLaunchRepository(db), scope, mvkt.NewClient(cfg.MvKT, &cfg.API), nil, fx, nil)
	identity := valuation.SupplyIdentity{Network: "basenet", Contract: valuationAsset, TokenID: "0"}
	read := func() valuation.Snapshot {
		s, ok, e := store.Current(ctx, identity)
		require.NoError(t, e)
		require.True(t, ok)
		return s
	}
	require.NoError(t, collector.RunOnce(ctx))
	s := read()
	require.Len(t, s.Valuations, 1)
	require.Equal(t, "4000000", s.Valuations[0].MarketCap.String())
	require.Equal(t, "5000000", s.Valuations[0].FDV.String())
	require.True(t, s.AsOf.Equal(fx.at))
	require.Equal(t, "3680000", s.Valuations[0].Conversions["eur"].MarketCap.String())
	balance.Store(9900000000)
	require.NoError(t, collector.RunOnce(ctx))
	s = read()
	require.Equal(t, "4010000", s.Valuations[0].MarketCap.String())
	// Removal does not erase the historical treasury obligation.
	doc.Launchpads = []string{}
	require.NoError(t, lookup.ApplyAllowlist(ctx, scopeURL, doc, true))
	require.NoError(t, collector.RunOnce(ctx))
	s = read()
	require.Equal(t, "4010000", s.Valuations[0].MarketCap.String())
	// Persistent metadata conflict must not clear itself on the second poll.
	scale.Store(8)
	for i := 0; i < 2; i++ {
		require.NoError(t, collector.RunOnce(ctx))
		s = read()
		require.Nil(t, s.Valuations[0].MarketCap)
		require.Equal(t, "decimals_conflict", s.Valuations[0].MarketCapQuality.Reason)
		require.Equal(t, "fresh", s.Supply.TotalQuality.Status)
	}
	scale.Store(6)
	require.NoError(t, collector.RunOnce(ctx))
	s = read()
	require.NotNil(t, s.Valuations[0].MarketCap)
	fail.Store(true)
	require.Error(t, collector.RunOnce(ctx))
	stale := read()
	require.Equal(t, s.AsOf, stale.AsOf)
	require.Equal(t, "stale", stale.Valuations[0].MarketCapQuality.Status)
	require.Equal(t, s.Valuations[0].MarketCapQuality.FreshUntil, stale.Valuations[0].MarketCapQuality.FreshUntil)
	fail.Store(false)
	hash.Store("replacement")
	require.NoError(t, collector.RunOnce(ctx))
	s = read()
	require.Equal(t, "replacement", s.Supply.Block.Hash)
	var invalidated int64
	require.NoError(t, db.Table("rwa_valuation_points").Where("invalidated").Count(&invalidated).Error)
	require.Positive(t, invalidated)
	// A verified head rollback below the previous observed block publishes a
	// new revision without trying to fetch a now-nonexistent previous block.
	beforeRevision, err := strconv.ParseInt(s.Revision, 10, 64)
	require.NoError(t, err)
	height.Store(998)
	require.NoError(t, collector.RunOnce(ctx))
	s = read()
	require.Equal(t, int64(996), s.Supply.Block.Level)
	afterRevision, err := strconv.ParseInt(s.Revision, 10, 64)
	require.NoError(t, err)
	require.Greater(t, afterRevision, beforeRevision)
	// A secondary pair without last never invokes primary (history is nil).
	market.ObservedAt = time.Now().UTC()
	market.LastPrice = nil
	require.NoError(t, marketRepo.SaveSnapshots(ctx, []prices.RWAMarketSnapshot{market}))
	require.NoError(t, collector.RunOnce(ctx))
	s = read()
	require.Nil(t, s.Valuations[0].MarketCap)
	require.Equal(t, "last", s.Valuations[0].PriceBasis)
	require.True(t, strings.Contains(s.Valuations[0].MarketCapQuality.Reason, "price"))
}
