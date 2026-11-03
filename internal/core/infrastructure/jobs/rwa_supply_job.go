package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"
	"quotes/internal/config"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/domain/valuation"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/interactions/mvkt"
	"quotes/internal/core/infrastructure/interactions/primary"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/metrics"
)

type RWASupplyJob struct {
	Config   *config.Config
	Store    *repositories.RWAValuationRepository
	Lookup   *repositories.LookupRepository
	Launches *repositories.LaunchRepository
	Scope    *equiteez.AllowlistProvider
	Chain    *mvkt.Client
	Primary  *primary.History
	FX       app.PriceConverter
	Logger   *zerolog.Logger
	stop     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
}

func NewRWASupplyJob(cfg *config.Config, store *repositories.RWAValuationRepository, lookup *repositories.LookupRepository, launches *repositories.LaunchRepository, scope *equiteez.AllowlistProvider, chain *mvkt.Client, history *primary.History, fx app.PriceConverter, logger *zerolog.Logger) *RWASupplyJob {
	if logger == nil {
		n := zerolog.Nop()
		logger = &n
	}
	c := cfg.RWASupply.WithDefaults(cfg.RWA.PricingNetwork())
	chain.WithSupplyLimits(mvkt.SupplyLimits{PageSize: c.PageSize, MaxRows: c.MaxLaunches, MaxResponseBytes: c.MaxResponseBytes})
	return &RWASupplyJob{Config: cfg, Store: store, Lookup: lookup, Launches: launches, Scope: scope, Chain: chain, Primary: history, FX: fx, Logger: logger, stop: make(chan struct{})}
}

func (j *RWASupplyJob) Start(ctx context.Context) {
	if !j.Config.RWASupply.Enabled {
		return
	}
	c := j.Config.RWASupply.WithDefaults(j.Config.RWA.PricingNetwork())
	interval := time.Duration(c.IntervalSeconds) * time.Second
	safeGo(&j.wg, j.Logger, "rwa_supply", func() {
		runTickerLoop(ctx, j.stop, interval, defaultJitter(interval), j.Logger, "rwa_supply", j.RunOnce)
	})
}
func (j *RWASupplyJob) Stop() { j.once.Do(func() { close(j.stop) }); j.wg.Wait() }

func (j *RWASupplyJob) RunOnce(ctx context.Context) error {
	c := j.Config.RWASupply.WithDefaults(j.Config.RWA.PricingNetwork())
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TickTimeoutSeconds)*time.Second)
	defer cancel()
	return j.Lookup.WithSupplyLock(ctx, func() error {
		scope, err := j.Scope.Snapshot(ctx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		chain, sourceErr := mvkt.PrepareSupplyContext(ctx, j.Chain, c.ExpectedChainID, int64(c.BlockLag), scope.TreasuryLaunchpads, now, time.Duration(c.IndexerLagSeconds)*time.Second)
		now = time.Now().UTC()
		if chain != nil && !chain.Block.Timestamp.IsZero() {
			metrics.RWASupplyLag.Set(now.Sub(chain.Block.Timestamp).Seconds())
		}
		// Delisting changes the current projection, not the validity of history.
		removed, err := j.Store.Delisted(ctx, j.Config.RWA.PricingNetwork(), scope.Document.BaseTokens)
		if err != nil {
			return err
		}
		for _, address := range removed {
			j.fail(ctx, address, mvkt.SupplyError("asset_delisted"), now)
		}
		pairs, err := j.Lookup.EnabledRWAPairs(ctx)
		if err != nil {
			return err
		}
		launches, err := j.Launches.EnabledLaunches(ctx, prices.SourceEquiteez)
		if err != nil {
			return err
		}
		var mu sync.Mutex
		failed := 0
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(c.Concurrency)
		for _, address := range scope.Document.BaseTokens {
			g.Go(func() error {
				tokenCtx, done := context.WithTimeout(gctx, time.Duration(c.TokenTimeoutSeconds)*time.Second)
				defer done()
				err := j.collect(tokenCtx, address, scope, chain, sourceErr, pairs, launches, now, false)
				publishCtx, publishCancel := context.WithTimeout(context.WithoutCancel(gctx), 3*time.Second)
				defer publishCancel()
				reason := ""
				if err != nil {
					reason = mvkt.SupplyReason(err)
					mu.Lock()
					failed++
					mu.Unlock()
					j.Logger.Warn().Err(err).Str("contract", address).Msg("rwa_supply_asset_failed")
					j.fail(publishCtx, address, err, now)
				}
				outcome := "success"
				if err != nil {
					outcome = "failure"
				}
				metrics.RWASupplyCollections.WithLabelValues(outcome).Inc()
				_ = j.Store.RecordAttempt(publishCtx, j.Config.RWA.PricingNetwork(), address, reason, now)
				return nil // one asset must not cancel the batch
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}
		if failed > 0 {
			metrics.RWASupplyCoverage.Set(float64(len(scope.Document.BaseTokens)-failed) / float64(len(scope.Document.BaseTokens)))
			return fmt.Errorf("RWA supply partial coverage: %d/%d failed", failed, len(scope.Document.BaseTokens))
		}
		metrics.RWASupplyCoverage.Set(1)
		return nil
	})
}

func (j *RWASupplyJob) collect(ctx context.Context, address string, scope equiteez.AllowlistSnapshot, chain *mvkt.SupplyContext, sourceErr error, pairs []prices.RWAPair, launches []prices.RWALaunch, asOf time.Time, historical bool) error {
	c := j.Config.RWASupply.WithDefaults(j.Config.RWA.PricingNetwork())
	policy := c.Policy()
	if sourceErr != nil {
		return sourceErr
	}
	clock := time.Now
	if historical {
		clock = func() time.Time { return chain.Block.Timestamp }
	}
	amounts, err := mvkt.ReadSupplyAmounts(ctx, j.Chain, address, chain, clock, time.Duration(c.IndexerLagSeconds)*time.Second)
	if err != nil {
		return err
	}
	id := valuation.SupplyIdentity{Network: j.Config.RWA.PricingNetwork(), Contract: address, TokenID: amounts.TokenID}
	old, found, err := j.Store.Current(ctx, id)
	if err != nil {
		return err
	}
	expected := int64(0)
	if found {
		expected, _ = strconv.ParseInt(old.Revision, 10, 64)
	}
	if found && !historical && old.Supply.Block.Level > 0 {
		reorg := old.Supply.Block.Level > chain.Head.Level
		if !reorg {
			previous, e := j.Chain.GetBlockIdentity(ctx, old.Supply.Block.Level)
			if e != nil {
				return e
			}
			reorg = previous.Hash != old.Supply.Block.Hash
		}
		if reorg {
			j.fail(ctx, address, mvkt.SupplyError("chain_reorg"), asOf)
			old, found, err = j.Store.Current(ctx, id)
			if err != nil {
				return err
			}
			if found {
				expected, _ = strconv.ParseInt(old.Revision, 10, 64)
			}
		} else if chain.Block.Level < old.Supply.Block.Level {
			return repositories.ErrValuationSuperseded
		}
	}
	meta, symbol, metaErr := j.Chain.SupplyMetadataAt(ctx, id, chain.Block)
	if metaErr != nil {
		meta.Reason = mvkt.SupplyReason(metaErr)
		if meta.Reason == "http_error" {
			meta.Reason = "metadata_unverified"
		}
	}
	if found && !historical {
		expectedScale := old.Supply.Metadata.Decimals
		if old.Supply.Metadata.ExpectedDecimals != nil {
			expectedScale = old.Supply.Metadata.ExpectedDecimals
		}
		if expectedScale != nil && meta.Decimals != nil && *expectedScale != *meta.Decimals {
			meta.Reason, meta.ExpectedDecimals = "decimals_conflict", expectedScale
		}
	}
	s := valuation.Supply{Identity: id, Block: chain.Block, IndexedHead: chain.Head.SupplyBlock, ObservedAt: asOf, Total: amounts.Total, Excluded: amounts.Excluded, Circulating: amounts.Circulating, Metadata: meta, ScopeVersion: scope.Version, ScopeFetchedAt: scope.FetchedAt, Source: "mvkt", Provenance: "observed"}
	if historical {
		s.Provenance = "reconstructed"
		s.IndexedHead = chain.Block // today's indexed head is not a historical valuation input
	}
	s.ScopeStatus = scope.SourceStatus
	s.TotalQuality = policy.Quality(chain.Block.Timestamp, asOf, "")
	s.CirculatingQuality = s.TotalQuality
	if amounts.CirculatingError != nil {
		s.CirculatingQuality = valuation.Missing(mvkt.SupplyReason(amounts.CirculatingError))
	}
	snapshot := valuation.Snapshot{Identity: id, Supply: s, AsOf: asOf, Valuations: []valuation.Point{}}
	var routes []valuation.Point
	if historical {
		routes, err = j.Store.RoutingAt(ctx, id, asOf, policy.Fresh)
		if err != nil {
			return err
		}
		if len(routes) == 0 {
			snapshot.Reason = "routing_unverified"
		}
	} else {
		for _, pair := range pairs {
			if pair.TokenAddr == address && scope.Document.PairAllowed(address, pair.QuoteAddr, pair.OrderbookAddr) {
				routes = append(routes, valuation.Point{Symbol: strings.ToLower(pair.BaseSymbol + "-" + pair.QuoteSymbol), PairID: pair.ID, Orderbook: pair.OrderbookAddr, Quote: valuation.Quote{Contract: pair.QuoteAddr, Symbol: strings.ToLower(pair.QuoteSymbol)}, Generation: pair.IndexerGeneration, PriceBasis: "last"})
			}
		}
		if len(routes) == 0 {
			for _, launch := range launches {
				if launch.TokenAddr == address {
					symbol = launch.BaseSymbol
					break
				}
			}
			if symbol != "" {
				routes = []valuation.Point{{Symbol: strings.ToLower(symbol) + "-usdt", PriceBasis: "primary_list_price"}}
			}
		}
	}
	seen := map[string]int{}
	for _, route := range routes {
		seen[route.Symbol]++
	}
	for _, route := range routes {
		p, e := j.valueRoute(ctx, s, scope, route, asOf, seen[route.Symbol] > 1)
		if e != nil {
			return e
		}
		snapshot.Valuations = append(snapshot.Valuations, p)
	}
	if len(snapshot.Valuations) == 0 {
		if snapshot.Reason == "" {
			snapshot.Reason = "price_missing"
		}
		if meta.Reason != "" {
			snapshot.Reason = meta.Reason
		}
	}
	if !historical {
		currentScope, e := j.Scope.Snapshot(ctx)
		if e != nil {
			return e
		}
		if currentScope.Version != scope.Version {
			return repositories.ErrValuationSuperseded
		}
	}
	verified, e := j.Chain.GetBlockIdentity(ctx, chain.Block.Level)
	if e != nil {
		return e
	}
	if verified.Hash != chain.Block.Hash {
		return mvkt.SupplyError("chain_reorg")
	}
	_, err = j.Store.Save(ctx, snapshot, expected, historical)
	if err == nil && historical {
		if snapshot.Reason != "" {
			return mvkt.SupplyError(snapshot.Reason)
		}
		usable := false
		for _, p := range snapshot.Valuations {
			if p.MarketCap != nil {
				usable = true
				break
			}
		}
		if !usable && len(snapshot.Valuations) > 0 {
			return mvkt.SupplyError(snapshot.Valuations[0].MarketCapQuality.Reason)
		}
	}
	return err
}

func (j *RWASupplyJob) convert(ctx context.Context, p *valuation.Point) {
	p.Conversions = map[string]valuation.Conversion{}
	for _, currency := range prices.AllSupportedCurrencies() {
		v := valuation.Conversion{MarketCapQuality: p.MarketCapQuality, FDVQuality: p.FDVQuality}
		if p.MarketCap == nil && p.FDV == nil {
			p.Conversions[string(currency)] = v
			continue
		}
		if j.FX == nil {
			v.MarketCapQuality = valuation.Missing("fx_unavailable")
			v.FDVQuality = v.MarketCapQuality
			p.Conversions[string(currency)] = v
			continue
		}
		fx, err := j.FX.Convert(ctx, prices.Token(p.Quote.Symbol), currency, decimal.NewFromInt(1), p.AsOf)
		if err != nil {
			v.MarketCapQuality = valuation.Missing("fx_unavailable")
			v.FDVQuality = v.MarketCapQuality
			p.Conversions[string(currency)] = v
			continue
		}
		v.Rate = &fx.Rate
		v.FXAt = &fx.RateTS
		f, e := fx.RateTS.Add(time.Duration(j.Config.Server.FXMaxStalenessSeconds)*time.Second), fx.RateTS.Add(app.FXHardStalenessCap)
		fq := valuation.Quality{Status: "fresh", FreshUntil: &f, ExpiresAt: &e}
		if fx.Identity {
			fq = valuation.Quality{Status: "fresh", FreshUntil: p.FDVQuality.FreshUntil, ExpiresAt: p.FDVQuality.ExpiresAt}
		}
		if fx.Stale {
			fq.Status = "stale"
			fq.Reason = "fx_stale"
		}
		v.MarketCapQuality = valuation.Combine(p.MarketCapQuality, fq)
		v.FDVQuality = valuation.Combine(p.FDVQuality, fq)
		if p.MarketCap != nil {
			x := p.MarketCap.Mul(fx.Rate)
			v.MarketCap = &x
		}
		if p.FDV != nil {
			x := p.FDV.Mul(fx.Rate)
			v.FDV = &x
		}
		p.Conversions[string(currency)] = v
	}
}

func (j *RWASupplyJob) fail(ctx context.Context, address string, cause error, now time.Time) {
	rows, err := j.Store.ForContract(ctx, j.Config.RWA.PricingNetwork(), address)
	if err != nil {
		return
	}
	reason := mvkt.SupplyReason(cause)
	if errors.Is(cause, repositories.ErrValuationSuperseded) {
		return
	}
	invalid := reason == "chain_reorg" || reason == "network_mismatch" || reason == "identity_ambiguous" || reason == "token_not_found" || reason == "asset_delisted"
	for _, s := range rows {
		expected, _ := strconv.ParseInt(s.Revision, 10, 64)
		s = s.At(now)
		s.CurrentOnly = true
		s.Reason = reason
		s.Invalidated = invalid
		degrade := func(q valuation.Quality) valuation.Quality {
			if invalid {
				return valuation.Missing(reason)
			}
			if q.Status == "fresh" {
				q.Status = "stale"
				q.Reason = reason
			}
			return q
		}
		s.Supply.TotalQuality = degrade(s.Supply.TotalQuality)
		s.Supply.CirculatingQuality = degrade(s.Supply.CirculatingQuality)
		for i := range s.Valuations {
			p := &s.Valuations[i]
			p.MarketCapQuality = degrade(p.MarketCapQuality)
			p.PriceQuality = degrade(p.PriceQuality)
			p.FDVQuality = degrade(p.FDVQuality)
			if invalid {
				p.MarketCap = nil
				p.FDV = nil
			}
			for k, v := range p.Conversions {
				v.MarketCapQuality = degrade(v.MarketCapQuality)
				v.FDVQuality = degrade(v.FDVQuality)
				if invalid {
					v.MarketCap = nil
					v.FDV = nil
				}
				p.Conversions[k] = v
			}
		}
		if _, err := j.Store.Save(ctx, s, expected, false); err != nil {
			j.Logger.Warn().Err(err).Msg("rwa_supply_failure_publish_failed")
		}
	}
}
