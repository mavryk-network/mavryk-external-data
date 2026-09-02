package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/interactions/equiteez"
	"quotes/internal/core/infrastructure/storage/repositories"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
)

// SyncRWAPairs discovers active RWA pairs from the Equiteez indexer and
// upserts them into `rwa_pairs`. The Equiteez allowlist is the source of
// truth: `token.in_allowlist=true` AND `orderbook.in_allowlist=true`.
//
// Semantics:
//   - INSERT new pairs with `enabled=true`.
//   - UPDATE existing pairs' metadata (`last_synced_at`, base/quote/token addr);
//     `enabled` is touched only to undo the sync's own disable — operator
//     overrides survive.
//   - Pairs that disappear from the allowlist get `enabled=false` with
//     disabled_reason='sync_missing' (soft disable; preserves history and the
//     synthetic `pair_id` FK, re-enabled when the pair reappears).
//
// Returns the number of pairs that ended up enabled (i.e. visible to the
// collector after the sync). Logs at info level for ops visibility.
func SyncRWAPairs(
	ctx context.Context,
	cfg *config.Config,
	lookup *repositories.LookupRepository,
	logger *zerolog.Logger,
) (int, error) {
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	log := logging.WithComponent(logger, "rwa_pair_sync")

	if !cfg.RWA.Enabled {
		log.Info().Msg("rwa_pair_sync_skipped_disabled")
		return 0, nil
	}
	if strings.TrimSpace(cfg.Equiteez.IndexerURL) == "" {
		log.Warn().Msg("rwa_pair_sync_skipped_no_indexer_url")
		return 0, nil
	}

	timeout := time.Duration(cfg.API.TimeoutSeconds) * time.Second
	client := equiteez.NewClient(cfg.Equiteez, &cfg.API, timeout, logger)

	tokens, err := client.GetAllowlistedTokensAndOrderbooks(ctx)
	if err != nil {
		return 0, fmt.Errorf("fetch allowlisted tokens: %w", err)
	}

	now := time.Now().UTC()
	keepIDs := make([]int64, 0, len(tokens))
	source := prices.SourceEquiteez
	upsertFailed := 0

	for _, tok := range tokens {
		if !tok.InAllowlist {
			continue // defensive — query already filters, but the field is human-toggleable
		}
		baseSymbol := deriveBaseSymbol(tok)
		for _, ob := range tok.Orderbooks {
			if !ob.InAllowlist {
				continue
			}
			if strings.TrimSpace(ob.Address) == "" {
				log.Warn().
					Str("token_addr", tok.Address).
					Msg("rwa_pair_sync_skipping_orderbook_no_address")
				continue
			}
			pair := prices.RWAPair{
				Source:        source,
				TokenAddr:     tok.Address,
				QuoteAddr:     ob.QuoteTokenAddress(),
				OrderbookAddr: ob.Address,
				BaseSymbol:    baseSymbol,
				QuoteSymbol:   ob.QuoteSymbol(),
			}
			if ob.ID > 0 {
				id := int32(ob.ID) //nolint:gosec // orderbook.id fits comfortably in int32
				pair.EquiteezOrderbookID = &id
			}
			id, err := lookup.UpsertRWAPair(ctx, pair, now)
			if err != nil {
				upsertFailed++
				log.Error().
					Err(err).
					Str("token_addr", tok.Address).
					Str("orderbook_addr", ob.Address).
					Msg("rwa_pair_sync_upsert_failed")
				continue
			}
			keepIDs = append(keepIDs, id)
		}
	}

	// Soft-disable missing pairs ONLY on a complete, non-empty view: an empty
	// allowlist or a failed upsert makes keepIDs unreliable and would disable
	// every enabled pair.
	var disabled int64
	if shouldDisableMissingPairs(len(tokens), len(keepIDs), upsertFailed) {
		disabled, err = lookup.DisableMissingRWAPairs(ctx, source, keepIDs)
		if err != nil {
			return 0, fmt.Errorf("disable missing pairs: %w", err)
		}
	} else {
		log.Warn().
			Int("token_count", len(tokens)).
			Int("upserted", len(keepIDs)).
			Int("upsert_failed", upsertFailed).
			Msg("rwa_pair_sync_skipping_disable_incomplete_view")
	}

	log.Info().
		Int("upserted", len(keepIDs)).
		Int64("disabled_missing", disabled).
		Int("token_count", len(tokens)).
		Int("upsert_failed", upsertFailed).
		Msg("rwa_pair_sync_completed")

	// Upstream had pairs and none landed (DB refusing writes): the tick must
	// count as failed, not stamp a last-success claiming the catalog is current.
	if upsertFailed > 0 && len(keepIDs) == 0 {
		return 0, fmt.Errorf("rwa pair sync: all %d upserts failed", upsertFailed)
	}
	return len(keepIDs), nil
}

// shouldDisableMissingPairs reports whether the view is complete enough to
// safely soft-disable absent pairs. Any of empty allowlist, nothing upserted,
// or a failed upsert means the keep-set under-represents the live pairs.
func shouldDisableMissingPairs(tokenCount, keepCount, upsertFailed int) bool {
	return tokenCount > 0 && keepCount > 0 && upsertFailed == 0
}

// deriveBaseSymbol extracts a human label for the base asset.
//
// Resolution order (first non-empty wins):
//  1. `token_metadata.symbol`  — TZIP-21/12 canonical field.
//  2. `metadata.symbol`        — some indexers expose the same data on the parent.
//  3. `token_metadata.name`    — fallback when symbol is omitted.
//  4. `metadata.name`.
//  5. `equiteez:<token_id>`    — for FA2 tokens with explicit ids.
//  6. shortened token contract — never returns the full KT1… in `base_symbol`,
//     so the column always carries something distinguishable in the UI.
//
// NOTE: a manual `UPDATE rwa_pairs SET base_symbol=...` does NOT survive the
// sync — UpsertRWAPair re-derives base/quote symbols on every pass.
func deriveBaseSymbol(tok equiteez.TokenWithOrderbooks) string {
	for _, raw := range [][]byte{tok.TokenMetadata, tok.Metadata} {
		if s := metadataField(raw, "symbol"); s != "" {
			return s
		}
	}
	for _, raw := range [][]byte{tok.TokenMetadata, tok.Metadata} {
		if s := metadataField(raw, "name"); s != "" {
			return s
		}
	}
	if tok.TokenID > 0 {
		return fmt.Sprintf("equiteez:%d", tok.TokenID)
	}
	return shortAddr(tok.Address)
}

// metadataField extracts a string field from a JSON blob. Tolerates null /
// invalid / non-object payloads (returns ""). Common keys: "symbol", "name".
func metadataField(raw []byte, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// shortAddr renders a Tezos address as KT1M3…UMoj (5+1+4 chars). Used as a
// last-resort base_symbol so the column never carries the full 36-char KT1…
// — an operator looking at the table sees something distinguishable.
func shortAddr(addr string) string {
	const head, tail = 5, 4
	if len(addr) <= head+tail+1 {
		return addr
	}
	return addr[:head] + "…" + addr[len(addr)-tail:]
}
