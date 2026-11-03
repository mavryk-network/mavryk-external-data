package jobs

import (
	"context"
	"fmt"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/rs/zerolog"
)

// SyncRWACatalog reconciles CRM membership independently from indexer readiness,
// then publishes metadata only from a ready, unchanged indexer generation.
func SyncRWACatalog(ctx context.Context, cfg *config.Config, lookup *repositories.LookupRepository, logger *zerolog.Logger, providers ...*equiteez.AllowlistProvider) error {
	if err := cfg.Equiteez.ValidateRecovery(); err != nil {
		return err
	}
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	ctx, cancel := equiteezOperationContext(ctx, cfg.API.TimeoutSeconds)
	defer cancel()
	client := equiteez.NewClient(cfg.Equiteez, &cfg.API, time.Duration(cfg.API.TimeoutSeconds)*time.Second, logger)
	provider := &equiteez.AllowlistProvider{Client: client, Store: lookup, URL: cfg.Equiteez.AllowlistURL}
	if len(providers) > 0 && providers[0] != nil {
		provider = providers[0]
	}
	snapshot, err := provider.Refresh(ctx)
	if err != nil {
		return err
	}
	if !cfg.RWA.Enabled || cfg.Equiteez.IndexerURL == "" {
		return nil
	}
	allowlist := snapshot.Document
	return lookup.WithEquiteezLock(ctx, func() error {
		generation, err := client.ReadyGeneration(ctx, cfg.Equiteez)
		if err != nil {
			return fmt.Errorf("equiteez catalog waiting for indexer: %w", err)
		}
		tokens, err := client.GetCatalogTokens(ctx, allowlist.BaseTokens)
		if err != nil {
			return err
		}
		rows, err := client.GetLaunchesByTokens(ctx, allowlist.BaseTokens)
		if err != nil {
			return err
		}
		pairs, launches := buildCatalog(tokens, rows, allowlist, generation, time.Now().UTC())
		if err = client.CheckGeneration(ctx, cfg.Equiteez, generation); err != nil {
			return err
		}
		if err = lookup.SaveEquiteezCatalog(ctx, pairs, launches, cfg.Equiteez, generation); err != nil {
			return err
		}
		logger.Info().Str("generation", generation).Int("pairs", len(pairs)).Int("launches", len(launches)).Time("allowlist_fetched_at", snapshot.FetchedAt).Msg("equiteez_catalog_synced")
		return nil
	})
}

func buildCatalog(tokens []equiteez.TokenWithOrderbooks, rows []equiteez.LaunchRow, a equiteez.ContractAllowlist, generation string, now time.Time) ([]prices.RWAPair, []prices.RWALaunch) {
	var pairs []prices.RWAPair
	symbols := make(map[string]string)
	for _, tok := range tokens {
		symbols[tok.Address] = deriveBaseSymbol(tok)
		for _, book := range tok.Orderbooks {
			if !a.PairAllowed(tok.Address, book.QuoteTokenAddress(), book.Address) || book.ID <= 0 || book.ID > 2147483647 {
				continue
			}
			id := int32(book.ID) //nolint:gosec // explicitly range checked above
			pairs = append(pairs, prices.RWAPair{Source: prices.SourceEquiteez, TokenAddr: tok.Address, QuoteAddr: book.QuoteTokenAddress(), OrderbookAddr: book.Address,
				BaseSymbol: symbols[tok.Address], QuoteSymbol: book.QuoteSymbol(), EquiteezOrderbookID: &id, Enabled: true, InAllowlist: true, IndexerGeneration: generation})
		}
	}
	// Filter campaigns before selection: a retired launchpad must not hide a
	// newer usable campaign of the same token.
	var allowed []equiteez.LaunchRow
	for _, row := range rows {
		_, _, quote, ok := row.BaseTierPrice()
		if ok && row.Token != nil && row.Launchpad != nil && a.LaunchAllowed(row.Token.Address, quote, row.Launchpad.Address) {
			allowed = append(allowed, row)
		}
	}
	var launches []prices.RWALaunch
	for addr, group := range groupLaunchesByToken(allowed) {
		if symbols[addr] == "" {
			continue
		}
		launch, ok := buildLaunch(group, symbols[addr], now)
		if !ok {
			continue
		}
		launch.InAllowlist = true
		launch.IndexerGeneration = generation
		for _, row := range group {
			if row.ID == launch.LaunchID {
				launch.LaunchpadAddr = row.Launchpad.Address
				break
			}
		}
		launches = append(launches, launch)
	}
	return pairs, launches
}

// Bound the whole operation, including DB work while holding the worker lock.
func equiteezOperationContext(ctx context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds <= 0 {
		seconds = 30
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}
