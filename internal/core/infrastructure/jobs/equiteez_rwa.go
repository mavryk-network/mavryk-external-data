package jobs

import (
	"context"
	"strconv"
	"sync"
	"time"

	"quotes/internal/config"
	apiprices "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"
	"quotes/internal/metrics"

	"github.com/rs/zerolog"
)

// EquiteezRWAJob polls Equiteez for the currently-known RWA pairs (loaded from
// the rwa_pairs table at startup) and writes one PricePoint per (pair, side)
// per tick into rwa_quote_prices.
//
// `rwa_pairs` is populated by the discovery sync (`SyncRWAPairs`); this
// collector only reads it. Operators who disable a pair (`enabled=false`) opt
// it out of polling; pairs whose orderbook is removed from Equiteez allowlist
// get auto-disabled by the next sync.
type EquiteezRWAJob struct {
	cfg    *config.Config
	repo   apiprices.Repository
	lookup *repositories.LookupRepository
	client *equiteez.Client
	logger *zerolog.Logger

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewEquiteezRWAJob wires the collector. Lookups are read once at Start; if you
// add new pairs while the service is running, restart the service to pick them up.
func NewEquiteezRWAJob(
	cfg *config.Config,
	repo apiprices.Repository,
	lookup *repositories.LookupRepository,
	log *zerolog.Logger,
) *EquiteezRWAJob {
	if log == nil {
		nop := zerolog.Nop()
		log = &nop
	}
	timeout := time.Duration(cfg.API.TimeoutSeconds) * time.Second
	return &EquiteezRWAJob{
		cfg:    cfg,
		repo:   repo,
		lookup: lookup,
		client: equiteez.NewClient(cfg.Equiteez, &cfg.API, timeout, log),
		logger: logging.WithComponent(log, "equiteez_rwa_job"),
		stopCh: make(chan struct{}),
	}
}

// Start spawns one ticker goroutine that polls every IntervalSeconds.
func (j *EquiteezRWAJob) Start(ctx context.Context) {
	if !j.cfg.RWA.Enabled {
		j.logger.Info().Msg("rwa_job_disabled_by_config")
		return
	}
	if j.cfg.Equiteez.IndexerURL == "" {
		j.logger.Warn().Msg("rwa_job_no_indexer_url_skipping")
		return
	}

	interval := time.Duration(j.cfg.RWA.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	j.logger.Info().
		Dur("interval", interval).
		Msg("rwa_job_starting")

	// NOTE: the ticker starts even when rwa_pairs is currently empty. Pairs are
	// resolved per tick (see collectTick), so a pair discovered later — by the
	// periodic RWAPairSyncJob or by an operator — is picked up without a restart.
	safeGo(&j.wg, j.logger, "rwa", func() {
		runTickerLoop(ctx, j.stopCh, interval, defaultJitter(interval), j.logger, "rwa", func(c context.Context) {
			j.collectTick(c)
		})
	})
}

// collectTick reloads the enabled pair list and collects one round.
//
// Reloading every tick (the shape EquiteezBackfillJob.tickAllPairs already uses)
// is what makes `rwa_pairs` changes take effect live: a newly listed asset starts
// being polled, and an operator setting enabled=false actually stops the writes.
// Previously the list was snapshotted at Start and frozen for the process
// lifetime, so both required a restart. The reload is one indexed SELECT per
// tick, negligible next to the outbound GraphQL round-trip that follows.
func (j *EquiteezRWAJob) collectTick(ctx context.Context) {
	pairs, err := j.lookup.RWAPairs(ctx)
	if err != nil {
		j.logger.Error().Err(err).Msg("rwa_load_pairs_failed")
		return
	}
	enabled := filterEnabledPairs(pairs, prices.SourceEquiteez)
	if len(enabled) == 0 {
		j.logger.Debug().Msg("rwa_no_enabled_pairs")
		return
	}
	j.collectOnce(ctx, enabled)
}

// Stop signals the goroutine and waits.
func (j *EquiteezRWAJob) Stop() {
	j.stopOnce.Do(func() { close(j.stopCh) })
	j.wg.Wait()
}

// collectOnce queries Equiteez for the configured token addresses, locates the
// orderbook the pair is associated with, and writes bid/ask/last per pair.
//
// We query by `token_addr` (the existing GetTokensWithOrderbooks shape) and
// then resolve the right orderbook by `orderbook_addr` — a single token may
// own multiple orderbooks (different quote currencies / regulatory tranches),
// so we match each pair against its specific orderbook contract.
func (j *EquiteezRWAJob) collectOnce(ctx context.Context, pairs []prices.RWAPair) {
	start := time.Now()
	defer func() {
		metrics.JobTickDurationSeconds.WithLabelValues("rwa", string(prices.SourceEquiteez), "all").
			Observe(time.Since(start).Seconds())
	}()

	// Group pairs by token address so one GraphQL request covers all pairs of
	// the same token; resolve the right orderbook on the response side.
	byTokenAddr, tokenAddresses := groupPairsByToken(pairs)
	if len(tokenAddresses) == 0 {
		j.logger.Debug().Msg("rwa_no_pairs_with_token_addr")
		return
	}

	// Resolve quote-currency decimals once per pair. Equiteez orderbooks
	// report prices in the smallest unit of the quote currency (micro-USDT
	// = 10^-6 USDT, etc.) normalized to "1 whole base token" — to get
	// human-readable prices we shift the decimal by `-quote.decimals`.
	// Pairs whose quote currency is not in the `tokens` registry are skipped
	// entirely: writing un-normalized data would store values 10^N too
	// large with no way for the consumer to know.
	pairDecimals := j.resolveQuoteDecimals(pairs)
	if len(pairDecimals) == 0 {
		j.logger.Warn().Msg("rwa_no_pairs_with_known_quote_decimals")
		return
	}

	tokens, err := j.client.GetTokensWithOrderbooks(ctx, tokenAddresses)
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("rwa", string(prices.SourceEquiteez), "all", "fetch").Inc()
		j.logger.Error().Err(err).Msg("rwa_fetch_failed")
		return
	}

	now := time.Now().UTC()
	points := buildPointsFromOrderbooks(tokens, byTokenAddr, pairDecimals, now)
	if len(points) == 0 {
		j.logger.Debug().Msg("rwa_no_points_collected")
		return
	}

	n, err := j.repo.Save(ctx, points)
	if err != nil {
		metrics.JobErrorsTotal.WithLabelValues("rwa", string(prices.SourceEquiteez), "all", "save").Inc()
		j.logger.Error().Err(err).Msg("rwa_save_failed")
		return
	}
	metrics.JobRowsAffectedTotal.WithLabelValues("rwa", string(prices.SourceEquiteez), "all").
		Add(float64(n))
	j.logger.Info().
		Int("pairs", len(pairs)).
		Int("points", len(points)).
		Int64("rows_affected", n).
		Msg("rwa_collected")
}

// groupPairsByToken returns a map (tokenAddr → pairs of that token) plus a
// deduplicated list of token addresses for the GraphQL filter.
func groupPairsByToken(pairs []prices.RWAPair) (map[string][]prices.RWAPair, []string) {
	byAddr := make(map[string][]prices.RWAPair, len(pairs))
	for _, p := range pairs {
		if p.TokenAddr == "" {
			continue
		}
		byAddr[p.TokenAddr] = append(byAddr[p.TokenAddr], p)
	}
	addrs := make([]string, 0, len(byAddr))
	for a := range byAddr {
		addrs = append(addrs, a)
	}
	return byAddr, addrs
}

// resolveQuoteDecimals builds (pair_id → quote.decimals) using the token
// registry that was loaded from `tokens` at startup. Pairs whose quote symbol
// is not registered are excluded — `collectOnce` then skips them rather than
// writing raw smallest-unit prices into `rwa_quote_prices`.
func (j *EquiteezRWAJob) resolveQuoteDecimals(pairs []prices.RWAPair) map[int64]int {
	out := make(map[int64]int, len(pairs))
	for _, p := range pairs {
		d, ok := lookupQuoteDecimals(p.QuoteSymbol)
		if !ok {
			j.logger.Warn().
				Int64("pair_id", p.ID).
				Str("quote_symbol", p.QuoteSymbol).
				Msg("rwa_unknown_quote_decimals_skipping_pair")
			continue
		}
		out[p.ID] = d
	}
	return out
}

// lookupQuoteDecimals consults the in-process token registry. Returns
// (decimals, true) only when the symbol is registered; (0, false) means
// "don't normalize because we don't know the scale".
func lookupQuoteDecimals(symbol string) (int, bool) {
	t, err := prices.NewToken(symbol)
	if err != nil {
		return 0, false
	}
	info, ok := prices.LookupToken(t)
	if !ok {
		return 0, false
	}
	return info.Decimals, true
}

// buildPointsFromOrderbooks walks the GraphQL response and emits one slice of
// PricePoints. The lookup `byTokenAddr` is consulted by token contract; the
// right orderbook is then matched on `OrderbookAddr` (a token may own multiple
// orderbooks, see refactoring_v2 §2.5). `pairDecimals` carries the per-pair
// quote-currency exponent for raw→human normalization.
func buildPointsFromOrderbooks(
	tokens []equiteez.TokenWithOrderbooks,
	byTokenAddr map[string][]prices.RWAPair,
	pairDecimals map[int64]int,
	now time.Time,
) []prices.PricePoint {
	var points []prices.PricePoint
	for _, tok := range tokens {
		pairsForToken := byTokenAddr[tok.Address]
		if len(pairsForToken) == 0 {
			continue
		}
		for _, ob := range tok.Orderbooks {
			ob := ob // capture
			pair, ok := matchPairForOrderbook(pairsForToken, ob.Address)
			if !ok {
				continue
			}
			decimals, ok := pairDecimals[pair.ID]
			if !ok {
				continue // already warned in resolveQuoteDecimals
			}
			points = append(points, orderbookToPoints(pair, &ob, decimals, now)...)
		}
	}
	return points
}

func matchPairForOrderbook(pairs []prices.RWAPair, orderbookAddr string) (prices.RWAPair, bool) {
	for _, p := range pairs {
		if p.OrderbookAddr == orderbookAddr {
			return p, true
		}
	}
	return prices.RWAPair{}, false
}

func filterEnabledPairs(in []prices.RWAPair, source prices.Source) []prices.RWAPair {
	out := make([]prices.RWAPair, 0, len(in))
	for _, p := range in {
		if p.Enabled && p.Source == source {
			out = append(out, p)
		}
	}
	return out
}

// orderbookToPoints renders one Equiteez orderbook into bid/ask/last
// PricePoints for one rwa_pair. `quoteDecimals` is the smallest-unit exponent
// of the orderbook's quote currency (e.g. 6 for USDT) — raw prices are
// shifted right by that many positions to produce human values:
//
//	raw 56_250_000 (micro-USDT)  →  56.25 USDT  with quoteDecimals=6
//
// Shift on decimal.Decimal is exact; no float rounding error.
func orderbookToPoints(pair prices.RWAPair, ob *equiteez.EquiteezOrderbook, quoteDecimals int, now time.Time) []prices.PricePoint {
	entityKey := strconv.FormatInt(pair.ID, 10)
	out := make([]prices.PricePoint, 0, 3)

	shift := -int32(quoteDecimals) //nolint:gosec // decimals is small (typically 6); int→int32 cannot overflow

	add := func(side string, v equiteez.FlexibleFloat) {
		price, reason, ok := mappablePrice(v, shift)
		if !ok {
			if reason != "" {
				metrics.IngestRowsDroppedTotal.
					WithLabelValues(string(pair.Source), entityKey, reason).Inc()
			}
			return
		}
		out = append(out, prices.PricePoint{
			Source:    pair.Source,
			EntityKey: entityKey,
			Timestamp: now,
			Metric:    side,
			Price:     price,
		})
	}
	add(string(prices.SideBid), ob.HighestBuyPrice)
	add(string(prices.SideAsk), ob.LowestSellPrice)
	add(string(prices.SideLast), ob.LastMatchedPrice)
	return out
}
