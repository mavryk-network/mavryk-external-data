//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	apitickers "quotes/internal/core/application/tickers"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/tickers"
	"quotes/internal/core/infrastructure/storage/entities"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The lookup rows seeded by 0009_seed.sql; token_tickers FKs into both.
const (
	tickerToken  prices.Token  = "mvrk"
	tickerSource prices.Source = "coingecko"
)

// Children first: token_tickers.exchange_id FKs into exchanges (0013), which
// has no seed row of its own — tests create what they reference.
func truncateTickers(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`TRUNCATE TABLE token_tickers`).Error)
	require.NoError(t, db.Exec(`DELETE FROM exchanges`).Error)
}

func tickerExchange(id, name string) tickers.Exchange {
	return tickers.Exchange{ID: id, Name: name, Kind: tickers.ExchangeKindCEX}
}

// tickerAt builds one (mvrk, coingecko) row. volume "" writes SQL NULL.
func tickerAt(exchangeID, target string, ts time.Time, price, volume string) tickers.Ticker {
	row := tickers.Ticker{
		Token:        tickerToken,
		Source:       tickerSource,
		ExchangeID:   exchangeID,
		TargetSymbol: target,
		Timestamp:    ts,
		LastPrice:    dec(price),
	}
	if volume != "" {
		v := dec(volume)
		row.VolumeBase = &v
	}
	return row
}

func requireDecEq(t *testing.T, want, got decimal.Decimal, label string) {
	t.Helper()
	require.Truef(t, want.Equal(got), "%s: want %s, got %s", label, want, got)
}

func snapshotRowFor(t *testing.T, snap tickers.Snapshot, exchangeID, target string) tickers.SnapshotRow {
	t.Helper()
	for _, r := range snap.Rows {
		if r.Exchange.ID == exchangeID && r.TargetSymbol == target {
			return r
		}
	}
	t.Fatalf("no snapshot row for (%s, %s) among %d rows", exchangeID, target, len(snap.Rows))
	return tickers.SnapshotRow{}
}

func distributionRowFor(t *testing.T, d tickers.Distribution, key string) tickers.DistributionRow {
	t.Helper()
	for _, r := range d.Rows {
		if r.Exchange.ID == key || r.TargetSymbol == key {
			return r
		}
	}
	t.Fatalf("no distribution row for %q among %d rows", key, len(d.Rows))
	return tickers.DistributionRow{}
}

func storedExchange(t *testing.T, db *gorm.DB, id string) (entities.ExchangeEntity, bool) {
	t.Helper()
	var ent entities.ExchangeEntity
	err := db.Where("id = ?", id).Take(&ent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entities.ExchangeEntity{}, false
	}
	require.NoError(t, err)
	return ent, true
}

func storedTickers(t *testing.T, db *gorm.DB, exchangeID string) []entities.TokenTickerEntity {
	t.Helper()
	var out []entities.TokenTickerEntity
	require.NoError(t, db.Where("exchange_id = ?", exchangeID).Order("ts ASC").Find(&out).Error)
	return out
}

func TestSaveTickers_EmptyInputIsNoop(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()

	n, err := repo.SaveSnapshot(ctx, nil, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	// The count stays pinned to ticker inserts; the job feeds it to invalidation.
	n, err = repo.SaveSnapshot(ctx, []tickers.Exchange{tickerExchange("solo", "Solo")}, nil)
	require.NoError(t, err)
	require.Equal(t, int64(0), n, "exchange upserts must not count toward the ticker delta")

	_, found := storedExchange(t, db, "solo")
	require.True(t, found, "exchange row must still land")
}

func TestSaveTickers_ExchangeUpsertRefreshesMetadata(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()

	seen := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{{
		ID: "gate", Name: "Gate", LogoURL: "   ", LastSeenAt: seen,
	}}, nil)
	require.NoError(t, err)

	got, found := storedExchange(t, db, "gate")
	require.True(t, found)
	require.Equal(t, "cex", got.Kind, "empty Kind must default to cex (0012 CHECK rejects '')")
	require.Nil(t, got.LogoURL, "whitespace-only logo must land as NULL")
	require.False(t, got.HasTradingIncentive)
	require.True(t, seen.Equal(got.LastSeenAt.UTC()))

	_, err = repo.SaveSnapshot(ctx, []tickers.Exchange{{
		ID: "gate", Name: "Gate.io", LogoURL: "https://cdn/gate.png",
		Kind: tickers.ExchangeKindDEX, HasTradingIncentive: true,
		LastSeenAt: seen.Add(time.Hour),
	}}, nil)
	require.NoError(t, err)

	got, found = storedExchange(t, db, "gate")
	require.True(t, found)
	require.Equal(t, "Gate.io", got.Name)
	require.NotNil(t, got.LogoURL)
	require.Equal(t, "https://cdn/gate.png", *got.LogoURL)
	require.Equal(t, "dex", got.Kind)
	require.True(t, got.HasTradingIncentive)
	require.True(t, seen.Add(time.Hour).Equal(got.LastSeenAt.UTC()), "last_seen_at must advance")

	// Zero LastSeenAt is filled in at write time rather than violating NOT NULL.
	before := time.Now().UTC().Add(-time.Second)
	_, err = repo.SaveSnapshot(ctx, []tickers.Exchange{tickerExchange("mexc", "MEXC")}, nil)
	require.NoError(t, err)
	got, found = storedExchange(t, db, "mexc")
	require.True(t, found)
	require.False(t, got.LastSeenAt.Before(before), "zero LastSeenAt must default to now")
}

func TestSaveTickers_ConflictKeepsFirstWrite(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Second)

	n, err := repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("binance", "Binance")},
		[]tickers.Ticker{tickerAt("binance", "usdt", ts, "1.5", "100")})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	// Same PK, different numbers: ON CONFLICT DO NOTHING, not DO UPDATE.
	n, err = repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("binance", "Binance")},
		[]tickers.Ticker{tickerAt("binance", "usdt", ts, "9.99", "42")})
	require.NoError(t, err)
	require.Equal(t, int64(0), n, "re-ingesting the same tick must report zero rows affected")

	rows := storedTickers(t, db, "binance")
	require.Len(t, rows, 1)
	requireDecEq(t, dec("1.5"), rows[0].LastPrice, "last_price")
	require.NotNil(t, rows[0].VolumeBase)
	requireDecEq(t, dec("100"), *rows[0].VolumeBase, "volume_24h_base")

	// A different ts is a different PK — inserts normally.
	n, err = repo.SaveSnapshot(ctx, nil,
		[]tickers.Ticker{tickerAt("binance", "usdt", ts.Add(5*time.Minute), "2", "200")})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

// CG can return a brand-new exchange and its ticker in one payload, so the FK
// forces exchanges to be written first inside the same tx.
func TestSaveTickers_NewExchangeInSameCallSatisfiesFK(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Second)

	n, err := repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("brandnew", "Brand New")},
		[]tickers.Ticker{tickerAt("brandnew", "usdt", ts, "1", "10")})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.Len(t, storedTickers(t, db, "brandnew"), 1)
}

func TestSaveTickers_FKViolationRollsBackTx(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Second)

	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{tickerExchange("kraken", "Kraken")}, nil)
	require.NoError(t, err)

	bad := tickerAt("ghostex", "usdt", ts, "1", "10")
	bad.Token = prices.Token("ghost") // no such row in tokens(symbol)
	n, err := repo.SaveSnapshot(ctx, []tickers.Exchange{
		{ID: "kraken", Name: "Kraken RENAMED"},
		tickerExchange("ghostex", "Ghost Exchange"),
	}, []tickers.Ticker{bad})
	require.Error(t, err)
	require.Equal(t, int64(0), n)
	require.Contains(t, err.Error(), "insert token_tickers")

	got, found := storedExchange(t, db, "kraken")
	require.True(t, found)
	require.Equal(t, "Kraken", got.Name, "exchange metadata must roll back with the failed ticker insert")

	_, found = storedExchange(t, db, "ghostex")
	require.False(t, found, "new exchange must not survive a rolled-back tx")
	require.Empty(t, storedTickers(t, db, "ghostex"))
}

func TestSaveTickers_TargetSymbolNormalised(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	ts := time.Now().UTC().Truncate(time.Second)

	_, err := repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("okx", "OKX")},
		[]tickers.Ticker{tickerAt("okx", "  USDT  ", ts, "1", "10")})
	require.NoError(t, err)

	rows := storedTickers(t, db, "okx")
	require.Len(t, rows, 1)
	require.Equal(t, "usdt", rows[0].TargetSymbol)

	// Normalisation precedes PK formation, so the padded variant collides.
	n, err := repo.SaveSnapshot(ctx, nil,
		[]tickers.Ticker{tickerAt("okx", "USDT", ts, "7", "70")})
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Len(t, storedTickers(t, db, "okx"), 1)
}

func TestLatestSnapshot_DistinctOnPerPair(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	rows := make([]tickers.Ticker, 0, 12)
	for i := 0; i < 5; i++ { // (binance, btc): 5 ticks, newest last
		ts := now.Add(-time.Duration(4-i) * 5 * time.Minute)
		rows = append(rows, tickerAt("binance", "btc", ts, decimal.NewFromInt(int64(i+1)).String(), "10"))
	}
	rows = append(rows,
		tickerAt("binance", "usdt", now.Add(-10*time.Minute), "9", "5"),
		tickerAt("kraken", "btc", now.Add(-10*time.Minute), "8", "5"),
	)
	// Neighbouring token/source rows must not leak into a (mvrk, coingecko) read.
	other := tickerAt("binance", "btc", now, "999", "999")
	other.Token = prices.Token("usdt")
	rows = append(rows, other)
	otherSource := tickerAt("binance", "btc", now, "888", "888")
	otherSource.Source = prices.SourceEquiteez
	rows = append(rows, otherSource)

	rows[0].TrustScore = "green"
	rows[0].TradeURL = "https://binance/mvrk-btc"
	rows[0].IsAnomaly = true
	spread := dec("0.25")
	rows[0].BidAskSpread = &spread

	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{
		{ID: "binance", Name: "Binance", LogoURL: "https://cdn/binance.png", Kind: tickers.ExchangeKindCEX},
		tickerExchange("kraken", "Kraken"),
	}, rows)
	require.NoError(t, err)

	snap, err := repo.LatestSnapshot(ctx, apitickers.LatestQuery{
		Token: tickerToken, Source: tickerSource, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Len(t, snap.Rows, 3, "one row per (exchange, target), not one per tick")

	got := snapshotRowFor(t, snap, "binance", "btc")
	require.True(t, now.Equal(got.Timestamp), "DISTINCT ON must keep the freshest ts, got %s", got.Timestamp)
	requireDecEq(t, dec("5"), got.LastPrice, "last_price of the freshest tick")
	require.Equal(t, "Binance", got.Exchange.Name)
	require.Equal(t, "https://cdn/binance.png", got.Exchange.LogoURL)
	require.Equal(t, tickers.ExchangeKindCEX, got.Exchange.Kind)
	require.False(t, got.IsStale)

	// Set on the OLDEST tick — seeing them means DISTINCT ON picked wrong.
	require.Empty(t, got.TrustScore)
	require.Empty(t, got.TradeURL)
	require.False(t, got.IsAnomaly)
	require.Nil(t, got.BidAskSpread)

	require.True(t, now.Equal(snap.Timestamp), "snapshot ts is the newest row's ts")
	require.Equal(t, tickerToken, snap.Token)
	require.Equal(t, tickerSource, snap.Source)
}

func TestLatestSnapshot_RowFieldsRoundTrip(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	row := tickerAt("gate", "usdt", now, "1.234567890123456789", "1234.5")
	spread := dec("0.0123456789")
	row.BidAskSpread = &spread
	row.TrustScore = " green "
	row.TradeURL = " https://gate/trade "
	row.IsAnomaly = true
	row.LastTradedAt = now.Add(-time.Minute)

	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{{
		ID: "gate", Name: "Gate", Kind: tickers.ExchangeKindDEX, HasTradingIncentive: true,
	}}, []tickers.Ticker{row})
	require.NoError(t, err)

	snap, err := repo.LatestSnapshot(ctx, apitickers.LatestQuery{
		Token: tickerToken, Source: tickerSource, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Len(t, snap.Rows, 1)

	got := snap.Rows[0]
	requireDecEq(t, dec("1.234567890123456789"), got.LastPrice, "numeric(38,18) must survive intact")
	require.NotNil(t, got.VolumeBase)
	requireDecEq(t, dec("1234.5"), *got.VolumeBase, "volume_24h_base")
	require.NotNil(t, got.BidAskSpread)
	requireDecEq(t, dec("0.0123456789"), *got.BidAskSpread, "bid_ask_spread_pct")
	require.Equal(t, "green", got.TrustScore)
	require.Equal(t, "https://gate/trade", got.TradeURL)
	require.True(t, got.IsAnomaly)
	require.Equal(t, tickers.ExchangeKindDEX, got.Exchange.Kind)
	require.True(t, got.Exchange.HasTradingIncentive)
	require.Nil(t, got.Change24hPct, "no anchor row ⇒ no 24h change")
}

type anchorFixture struct {
	ago   time.Duration
	price string
}

// The LATERAL anchor is the freshest row inside [latest.ts-25h, latest.ts-24h],
// inclusive at both edges; the -25h floor stops a post-gap anchor of arbitrary
// age from being reported as a "24h" change. Every anchor here is older than the
// stale fence, which lives in the `latest` CTE and must not blind the LATERAL.
func TestLatestSnapshot_LateralAnchorBracket(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	cases := []struct {
		name       string
		exchangeID string
		history    []anchorFixture
		wantChange string // "" ⇒ nil
	}{
		{
			name:       "freshest anchor at or before -24h wins",
			exchangeID: "multi",
			history: []anchorFixture{
				{23 * time.Hour, "50"},  // too recent to anchor
				{24 * time.Hour, "100"}, // the anchor
				{25 * time.Hour, "10"},  // older, loses to -24h
			},
			wantChange: "10",
		},
		{
			name:       "exactly -24h is inclusive",
			exchangeID: "edgein",
			history:    []anchorFixture{{24 * time.Hour, "100"}},
			wantChange: "10",
		},
		{
			name:       "one microsecond inside 24h is excluded",
			exchangeID: "edgeout",
			history:    []anchorFixture{{24*time.Hour - time.Microsecond, "100"}},
			wantChange: "",
		},
		{
			name:       "exactly -25h anchors when nothing newer qualifies",
			exchangeID: "edge25",
			history:    []anchorFixture{{25 * time.Hour, "100"}},
			wantChange: "10",
		},
		{
			name:       "anchor older than the bracket yields no percentage",
			exchangeID: "ancient",
			history:    []anchorFixture{{40 * time.Hour, "100"}},
			wantChange: "",
		},
		{
			name:       "no history at all",
			exchangeID: "nohist",
			wantChange: "",
		},
		{
			name:       "zero anchor price yields no percentage",
			exchangeID: "zeroanchor",
			history:    []anchorFixture{{24 * time.Hour, "0"}},
			wantChange: "",
		},
	}

	var exchanges []tickers.Exchange
	var rows []tickers.Ticker
	for _, tc := range cases {
		exchanges = append(exchanges, tickerExchange(tc.exchangeID, tc.exchangeID))
		rows = append(rows, tickerAt(tc.exchangeID, "usdt", now, "110", "10"))
		for _, h := range tc.history {
			rows = append(rows, tickerAt(tc.exchangeID, "usdt", now.Add(-h.ago), h.price, "10"))
		}
	}
	_, err := repo.SaveSnapshot(ctx, exchanges, rows)
	require.NoError(t, err)

	snap, err := repo.LatestSnapshot(ctx, apitickers.LatestQuery{
		Token: tickerToken, Source: tickerSource, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Len(t, snap.Rows, len(cases), "stale fence must keep exactly the fresh row of each pair")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := snapshotRowFor(t, snap, tc.exchangeID, "usdt")
			require.True(t, now.Equal(got.Timestamp))
			if tc.wantChange == "" {
				require.Nil(t, got.Change24hPct)
				return
			}
			require.NotNil(t, got.Change24hPct)
			requireDecEq(t, dec(tc.wantChange), *got.Change24hPct, "change_24h_pct")
		})
	}
}

// The sort key is base volume alone: multiplying by last_price would mix quote
// units, ranking a BTC-quoted market ~60000x below a USDT one at equal volume.
func TestLatestSnapshot_OrderedByVolumeThenIDs(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	exchanges := []tickers.Exchange{}
	for _, id := range []string{"highprice", "highvol", "novol", "tie1", "tie2", "zerovol"} {
		exchanges = append(exchanges, tickerExchange(id, id))
	}
	rows := []tickers.Ticker{
		tickerAt("highvol", "usdt", now.Add(-10*time.Minute), "1", "1000"),   // volume 1000
		tickerAt("highprice", "usdt", now.Add(-10*time.Minute), "100", "20"), // volume 20
		tickerAt("tie1", "usdt", now.Add(-10*time.Minute), "5", "100"),
		tickerAt("tie1", "btc", now.Add(-10*time.Minute), "5", "100"),
		tickerAt("tie2", "usdt", now, "5", "100"),
		tickerAt("novol", "usdt", now.Add(-10*time.Minute), "5", ""), // NULL volume
		tickerAt("zerovol", "usdt", now.Add(-10*time.Minute), "5", "0"),
	}
	_, err := repo.SaveSnapshot(ctx, exchanges, rows)
	require.NoError(t, err)

	snap, err := repo.LatestSnapshot(ctx, apitickers.LatestQuery{
		Token: tickerToken, Source: tickerSource, StaleAfter: time.Hour,
	})
	require.NoError(t, err)

	want := [][2]string{
		{"highvol", "usdt"},   // 1000 — a 100x higher price does not buy rank
		{"tie1", "btc"},       // 100, exchange_id then target_symbol ASC
		{"tie1", "usdt"},      // 100
		{"tie2", "usdt"},      // 100
		{"highprice", "usdt"}, // 20
		{"novol", "usdt"},     // NULL volume ⇒ COALESCE 0, sorts last
		{"zerovol", "usdt"},   // 0
	}
	require.Len(t, snap.Rows, len(want))
	for i, w := range want {
		require.Equalf(t, w[0], snap.Rows[i].Exchange.ID, "row %d exchange_id", i)
		require.Equalf(t, w[1], snap.Rows[i].TargetSymbol, "row %d target_symbol", i)
	}
	require.Nil(t, snapshotRowFor(t, snap, "novol", "usdt").VolumeBase)
	require.True(t, now.Equal(snap.Timestamp), "snapshot ts is the newest ts across rows, not the first row's")
}

func TestLatestSnapshot_StaleFence(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	_, err := repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("freshex", "Fresh"), tickerExchange("staleex", "Stale")},
		[]tickers.Ticker{
			tickerAt("freshex", "usdt", now.Add(-5*time.Minute), "1", "10"),
			tickerAt("staleex", "usdt", now.Add(-3*time.Hour), "2", "500"),
		})
	require.NoError(t, err)

	base := apitickers.LatestQuery{Token: tickerToken, Source: tickerSource, StaleAfter: time.Hour}

	snap, err := repo.LatestSnapshot(ctx, base)
	require.NoError(t, err)
	require.Len(t, snap.Rows, 1, "rows older than StaleAfter are hidden by default")
	require.Equal(t, "freshex", snap.Rows[0].Exchange.ID)
	require.False(t, snap.Rows[0].IsStale)
	require.True(t, now.Add(-5*time.Minute).Equal(snap.Timestamp))

	withStale := base
	withStale.IncludeStale = true
	snap, err = repo.LatestSnapshot(ctx, withStale)
	require.NoError(t, err)
	require.Len(t, snap.Rows, 2)
	require.True(t, snapshotRowFor(t, snap, "staleex", "usdt").IsStale)
	require.False(t, snapshotRowFor(t, snap, "freshex", "usdt").IsStale)

	noFence := base
	noFence.StaleAfter = 0
	snap, err = repo.LatestSnapshot(ctx, noFence)
	require.NoError(t, err)
	require.Len(t, snap.Rows, 2, "StaleAfter=0 disables the fence even with IncludeStale=false")
	require.False(t, snapshotRowFor(t, snap, "staleex", "usdt").IsStale,
		"with no staleness window nothing can be flagged stale")
}

func TestLatestSnapshot_NoRowsIsNotAnError(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)

	snap, err := repo.LatestSnapshot(context.Background(), apitickers.LatestQuery{
		Token: tickerToken, Source: tickerSource, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Empty(t, snap.Rows)
	require.True(t, snap.Timestamp.IsZero())
}

// volume_24h_base is a rolling snapshot, not a delta: summing every tick of a
// pair would multiply the reported volume by the tick count.
func TestVolumeDistribution_NoDoubleCount(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	rows := make([]tickers.Ticker, 0, 12)
	for i := 0; i < 12; i++ { // one hour of 5-minute ticks
		ts := now.Add(-time.Duration(11-i) * 5 * time.Minute)
		rows = append(rows, tickerAt("binance", "btc", ts, "1", "100"))
	}
	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{tickerExchange("binance", "Binance")}, rows)
	require.NoError(t, err)

	for _, groupBy := range []tickers.GroupBy{tickers.GroupByExchange, tickers.GroupByTarget} {
		t.Run(string(groupBy), func(t *testing.T) {
			d, err := repo.VolumeDistribution(ctx, apitickers.DistributionQuery{
				Token: tickerToken, Source: tickerSource, GroupBy: groupBy, StaleAfter: 2 * time.Hour,
			})
			require.NoError(t, err)
			require.Len(t, d.Rows, 1)
			requireDecEq(t, dec("100"), d.Total, "total volume (12 ticks × 100 must not become 1200)")
			requireDecEq(t, dec("100"), d.Rows[0].VolumeBase, "group volume")
			requireDecEq(t, dec("100"), d.Rows[0].SharePct, "share_pct")
		})
	}
}

func TestVolumeDistribution_GroupByExchange(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{
		{ID: "alpha", Name: "Alpha", LogoURL: "https://cdn/alpha.png", Kind: tickers.ExchangeKindDEX},
		tickerExchange("beta", "Beta"),
	}, []tickers.Ticker{
		tickerAt("alpha", "usdt", now, "1", "60"),
		tickerAt("alpha", "btc", now, "1", "20"),
		tickerAt("beta", "usdt", now, "1", "20"),
	})
	require.NoError(t, err)

	d, err := repo.VolumeDistribution(ctx, apitickers.DistributionQuery{
		Token: tickerToken, Source: tickerSource,
		GroupBy: tickers.GroupByExchange, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, tickers.GroupByExchange, d.GroupBy)
	require.Len(t, d.Rows, 2)
	requireDecEq(t, dec("100"), d.Total, "total")

	require.Equal(t, "alpha", d.Rows[0].Exchange.ID, "ordered by volume DESC")
	require.Equal(t, "beta", d.Rows[1].Exchange.ID)

	alpha := d.Rows[0]
	requireDecEq(t, dec("80"), alpha.VolumeBase, "alpha sums both of its targets")
	requireDecEq(t, dec("80"), alpha.SharePct, "share_pct")
	require.Equal(t, "Alpha", alpha.Exchange.Name)
	require.Equal(t, "https://cdn/alpha.png", alpha.Exchange.LogoURL)
	require.Equal(t, tickers.ExchangeKindDEX, alpha.Exchange.Kind)
	require.Empty(t, alpha.TargetSymbol, "target_symbol is meaningless when grouping by exchange")

	require.Empty(t, d.Rows[1].Exchange.LogoURL, "NULL logo comes back as empty, not a panic")

	sum := decimal.Zero
	for _, r := range d.Rows {
		sum = sum.Add(r.SharePct)
	}
	require.Truef(t, sum.Sub(dec("100")).Abs().LessThan(dec("0.000001")), "shares must total 100, got %s", sum)
}

func TestVolumeDistribution_GroupByTarget(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	_, err := repo.SaveSnapshot(ctx, []tickers.Exchange{
		{ID: "alpha", Name: "Alpha", LogoURL: "https://cdn/alpha.png", Kind: tickers.ExchangeKindDEX},
		tickerExchange("beta", "Beta"),
	}, []tickers.Ticker{
		tickerAt("alpha", "usdt", now, "1", "60"),
		tickerAt("alpha", "btc", now, "1", "20"),
		tickerAt("beta", "usdt", now, "1", "20"),
	})
	require.NoError(t, err)

	d, err := repo.VolumeDistribution(ctx, apitickers.DistributionQuery{
		Token: tickerToken, Source: tickerSource,
		GroupBy: tickers.GroupByTarget, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Equal(t, tickers.GroupByTarget, d.GroupBy)
	require.Len(t, d.Rows, 2)
	requireDecEq(t, dec("100"), d.Total, "total")

	require.Equal(t, "usdt", d.Rows[0].TargetSymbol, "ordered by volume DESC")
	require.Equal(t, "btc", d.Rows[1].TargetSymbol)

	usdt := d.Rows[0]
	requireDecEq(t, dec("80"), usdt.VolumeBase, "usdt sums across exchanges")
	requireDecEq(t, dec("80"), usdt.SharePct, "share_pct")
	require.Equal(t, tickers.Exchange{}, usdt.Exchange, "exchange columns stay zero-valued when grouping by target")

	sum := d.Rows[0].SharePct.Add(d.Rows[1].SharePct)
	require.Truef(t, sum.Sub(dec("100")).Abs().LessThan(dec("0.000001")), "shares must total 100, got %s", sum)
}

func TestVolumeDistribution_UnsupportedGroupBy(t *testing.T) {
	db := openGorm(t)
	repo := repositories.NewTickerRepository(db)

	for _, g := range []tickers.GroupBy{"", "trust_score", "EXCHANGE"} {
		_, err := repo.VolumeDistribution(context.Background(), apitickers.DistributionQuery{
			Token: tickerToken, Source: tickerSource, GroupBy: g, StaleAfter: time.Hour,
		})
		require.Errorf(t, err, "group_by %q must be rejected before hitting SQL", g)
		require.Contains(t, err.Error(), "unsupported group_by")
	}
}

func TestVolumeDistribution_NilAndZeroVolumes(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	q := apitickers.DistributionQuery{
		Token: tickerToken, Source: tickerSource,
		GroupBy: tickers.GroupByExchange, StaleAfter: time.Hour,
	}

	_, err := repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("novol", "No Volume"), tickerExchange("zerovol", "Zero Volume")},
		[]tickers.Ticker{
			tickerAt("novol", "usdt", now, "1", ""),
			tickerAt("zerovol", "usdt", now, "1", "0"),
		})
	require.NoError(t, err)

	d, err := repo.VolumeDistribution(ctx, q)
	require.NoError(t, err)
	require.True(t, d.Total.IsZero(), "SUM over NULL/0 volumes is 0, not NULL")
	require.Empty(t, d.Rows, "nothing to bucket ⇒ no pie slices")
	require.False(t, d.Timestamp.IsZero(), "distribution ts is stamped even when empty")

	_, err = repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("hasvol", "Has Volume")},
		[]tickers.Ticker{tickerAt("hasvol", "usdt", now, "1", "50")})
	require.NoError(t, err)

	d, err = repo.VolumeDistribution(ctx, q)
	require.NoError(t, err)
	requireDecEq(t, dec("50"), d.Total, "total")
	require.Len(t, d.Rows, 1, "zero-volume groups are dropped, not rendered as 0% slices")
	require.Equal(t, "hasvol", d.Rows[0].Exchange.ID)
	requireDecEq(t, dec("100"), d.Rows[0].SharePct, "share_pct")

	// A pair whose freshest tick lost its volume contributes nothing.
	_, err = repo.SaveSnapshot(ctx, nil,
		[]tickers.Ticker{tickerAt("hasvol", "usdt", now.Add(time.Minute), "1", "")})
	require.NoError(t, err)
	d, err = repo.VolumeDistribution(ctx, q)
	require.NoError(t, err)
	require.True(t, d.Total.IsZero(), "only the freshest row per pair is read")
	require.Empty(t, d.Rows)
}

// Distribution has no IncludeStale escape hatch (unlike LatestQuery): a stale
// pair is simply gone from the pie.
func TestVolumeDistribution_StaleRowsAlwaysExcluded(t *testing.T) {
	db := openGorm(t)
	truncateTickers(t, db)
	repo := repositories.NewTickerRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	_, err := repo.SaveSnapshot(ctx,
		[]tickers.Exchange{tickerExchange("freshex", "Fresh"), tickerExchange("staleex", "Stale")},
		[]tickers.Ticker{
			tickerAt("freshex", "usdt", now.Add(-5*time.Minute), "1", "70"),
			tickerAt("staleex", "usdt", now.Add(-3*time.Hour), "1", "30"),
		})
	require.NoError(t, err)

	d, err := repo.VolumeDistribution(ctx, apitickers.DistributionQuery{
		Token: tickerToken, Source: tickerSource,
		GroupBy: tickers.GroupByExchange, StaleAfter: time.Hour,
	})
	require.NoError(t, err)
	require.Len(t, d.Rows, 1)
	require.Equal(t, "freshex", d.Rows[0].Exchange.ID)
	requireDecEq(t, dec("70"), d.Total, "stale volume must not inflate the total")
	requireDecEq(t, dec("100"), d.Rows[0].SharePct, "shares are of the fresh total only")

	d, err = repo.VolumeDistribution(ctx, apitickers.DistributionQuery{
		Token: tickerToken, Source: tickerSource,
		GroupBy: tickers.GroupByExchange, StaleAfter: 0,
	})
	require.NoError(t, err)
	require.Len(t, d.Rows, 2, "StaleAfter=0 disables the fence")
	requireDecEq(t, dec("100"), d.Total, "total")
	requireDecEq(t, dec("30"), distributionRowFor(t, d, "staleex").VolumeBase, "staleex volume")
}
