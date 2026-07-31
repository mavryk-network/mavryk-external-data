package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
	"github.com/shopspring/decimal"
)

// defaultLaunchQuoteDecimals is the fallback when the payment currency is not in
// the token registry. The launchpad quotes in USDT-class stablecoins (6
// decimals) and the orderbook read path makes the same assumption for an
// unregistered quote side — a wrong guess only mis-scales a display price, and
// the alternative (dropping the asset) hides it from the catalog entirely.
const defaultLaunchQuoteDecimals = 6

// SyncRWALaunches mirrors the Equiteez launchpad into `rwa_launches`: for every
// allowlisted token it picks the surfaced launch and stores its base-tier price
// and sale progress.
//
// This is what makes a primary-issuance asset visible to GET /v1/rwa. Such a
// token has no orderbook — XAUG / MCDX / KHBE are allowlisted with an active
// launch and zero orderbooks — so SyncRWAPairs produces no row for it and the
// collector never sees it.
//
// Returns the number of launches stored. Per-token failures are logged and
// skipped so one bad launch cannot abort the catalog.
func SyncRWALaunches(
	ctx context.Context,
	cfg *config.Config,
	launches *repositories.LaunchRepository,
	logger *zerolog.Logger,
) (int, error) {
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	log := logging.WithComponent(logger, "rwa_launch_sync")

	if !cfg.RWA.Enabled {
		log.Info().Msg("rwa_launch_sync_skipped_disabled")
		return 0, nil
	}
	if strings.TrimSpace(cfg.Equiteez.IndexerURL) == "" {
		log.Warn().Msg("rwa_launch_sync_skipped_no_indexer_url")
		return 0, nil
	}
	if launches == nil {
		return 0, fmt.Errorf("launch repository is required")
	}

	timeout := time.Duration(cfg.API.TimeoutSeconds) * time.Second
	client := equiteez.NewClient(cfg.Equiteez, &cfg.API, timeout, logger)

	tokens, err := client.GetAllowlistedTokensAndOrderbooks(ctx)
	if err != nil {
		return 0, fmt.Errorf("fetch allowlisted tokens: %w", err)
	}

	// Base symbol per address, so the stored launch is addressable as
	// `{base}-{quote}` exactly like an orderbook pair.
	baseSymbols := make(map[string]string, len(tokens))
	addresses := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		addr := strings.TrimSpace(tok.Address)
		if addr == "" || !tok.InAllowlist {
			continue
		}
		baseSymbols[addr] = deriveBaseSymbol(tok)
		addresses = append(addresses, addr)
	}
	if len(addresses) == 0 {
		log.Debug().Msg("rwa_launch_sync_no_allowlisted_tokens")
		return 0, nil
	}

	rows, err := client.GetLaunchesByTokens(ctx, addresses)
	if err != nil {
		return 0, fmt.Errorf("fetch launchpad launches: %w", err)
	}

	now := time.Now().UTC()
	byToken := groupLaunchesByToken(rows)
	stored, skipped := 0, 0
	for addr, tokenRows := range byToken {
		launch, ok := buildLaunch(tokenRows, baseSymbols[addr], now)
		if !ok {
			skipped++
			log.Debug().Str("token_addr", addr).Msg("rwa_launch_sync_skipped_no_usable_launch")
			continue
		}
		if err := launches.Upsert(ctx, launch, now); err != nil {
			skipped++
			log.Error().Err(err).Str("token_addr", addr).Msg("rwa_launch_sync_upsert_failed")
			continue
		}
		stored++
	}

	log.Info().
		Int("tokens", len(addresses)).
		Int("launches", len(rows)).
		Int("stored", stored).
		Int("skipped", skipped).
		Msg("rwa_launch_sync_completed")
	return stored, nil
}

// groupLaunchesByToken buckets rows by token address, dropping rows with no
// token reference (the join is nullable in the indexer schema).
func groupLaunchesByToken(rows []equiteez.LaunchRow) map[string][]equiteez.LaunchRow {
	out := make(map[string][]equiteez.LaunchRow)
	for _, r := range rows {
		if r.Token == nil {
			continue
		}
		addr := strings.TrimSpace(r.Token.Address)
		if addr == "" {
			continue
		}
		out[addr] = append(out[addr], r)
	}
	return out
}

// buildLaunch selects the surfaced launch for one token and maps it to the
// domain type: base-tier price (decimals applied) plus raw sale progress.
//
// ok=false when the token has no launch quoting a usable price — storing a
// priceless asset would surface a null-price row in the catalog for no gain.
func buildLaunch(rows []equiteez.LaunchRow, baseSymbol string, now time.Time) (prices.RWALaunch, bool) {
	if len(rows) == 0 {
		return prices.RWALaunch{}, false
	}
	sel := make([]prices.LaunchSelectable, len(rows))
	for i, r := range rows {
		sel[i] = prices.LaunchSelectable{
			Status:     r.Status,
			IsPaused:   r.IsPaused,
			SaleStart:  r.SaleStart.Ptr(),
			SaleEnd:    r.SaleEnd.Ptr(),
			SaleClosed: r.SaleClosed.Ptr(),
			UpdatedAt:  r.UpdatedAt.Value(),
		}
	}
	idx, ok := prices.SelectLaunch(sel, now)
	if !ok {
		return prices.RWALaunch{}, false
	}
	row := rows[idx]

	rawPrice, currency, quoteAddr, hasPrice := row.BaseTierPrice()
	if !hasPrice {
		return prices.RWALaunch{}, false
	}
	quoteSymbol := strings.ToLower(strings.TrimSpace(currency))
	decimals, known := lookupQuoteDecimals(quoteSymbol)
	if !known {
		decimals = defaultLaunchQuoteDecimals
	}
	price, priceOK := prices.LaunchHumanPrice(rawPrice, decimals)
	if !priceOK {
		return prices.RWALaunch{}, false
	}

	tokenID := 0
	addr := ""
	if row.Token != nil {
		tokenID = row.Token.TokenID
		addr = strings.TrimSpace(row.Token.Address)
	}

	return prices.RWALaunch{
		Source:          prices.SourceEquiteez,
		TokenAddr:       addr,
		TokenID:         tokenID,
		LaunchID:        row.ID,
		Name:            row.Name,
		Status:          prices.LaunchStatusString(row.Status),
		Active:          prices.LaunchActive(sel[idx], now),
		BaseSymbol:      strings.ToLower(strings.TrimSpace(baseSymbol)),
		QuoteSymbol:     quoteSymbol,
		QuoteAddr:       quoteAddr,
		Price:           price,
		TotalBought:     decimalOrZero(row.TotalBought.String()),
		MaxAmountCap:    decimalOrZero(row.MaxAmountCap.String()),
		ProgressPercent: prices.ProgressPercent(row.TotalBought.String(), row.MaxAmountCap.String()),
		SaleStart:       row.SaleStart.Ptr(),
		SaleEnd:         row.SaleEnd.Ptr(),
		SaleClosed:      row.SaleClosed.Ptr(),
		Enabled:         true,
	}, true
}

// decimalOrZero parses a raw-nat string, yielding zero for empty/malformed
// input. Raw amounts are informational (progress is precomputed), so a bad
// value must not drop the asset from the catalog.
func decimalOrZero(raw string) decimal.Decimal {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero
	}
	return v
}
