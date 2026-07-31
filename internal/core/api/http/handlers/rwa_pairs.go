package handlers

import (
	"context"
	"sort"
	"strings"

	"quotes/internal/core/api/http/common"
	"quotes/internal/core/domain/prices"
	"quotes/internal/core/infrastructure/storage/repositories"

	"github.com/gin-gonic/gin"
)

// RWAPairsLister is the read-only contract the discovery handler needs:
// every enabled `(base, quote, source)` triple, in a stable order. Kept
// separate from PairLookup so RWAPriceDeps stays narrow.
type RWAPairsLister interface {
	EnabledRWAPairs(ctx context.Context) ([]prices.RWAPair, error)
}

// RWAPairsDeps wires the discovery endpoint.
type RWAPairsDeps struct {
	Lookup RWAPairsLister
	// Launches is optional: primary-issuance assets joining the catalog so a
	// client discovers every symbol /v1/rwa/:symbol serves, not only the traded
	// ones. Nil keeps the previous pairs-only behaviour.
	Launches RWALaunchLister
	// Source scopes the launch catalog (EnabledLaunches is per-source);
	// typically prices.SourceEquiteez.
	Source prices.Source
}

// rwaPairDTO is the on-wire shape of one catalog entry. Lowercased fields
// keep the contract self-consistent with `/v1/rwa/:symbol` parsing
// (parseRWASymbol lowercases on the way in).
//
// The address fields serve consumers that build transactions: `token_addr`
// (the RWA token), `quote_addr` (the settlement token to approve/spend), and
// `orderbook_addr` (the escrow contract). All three are nullable — a pair not
// yet re-synced after migration 0017 has no quote_addr, and a primary-market
// asset has no escrow at all (its payment flow is the launchpad's own domain),
// so its quote_addr / orderbook_addr are always null.
type rwaPairDTO struct {
	Symbol        string  `json:"symbol"`
	Base          string  `json:"base"`
	Quote         string  `json:"quote"`
	Market        string  `json:"market"`
	TokenAddr     *string `json:"token_addr"`
	QuoteAddr     *string `json:"quote_addr"`
	OrderbookAddr *string `json:"orderbook_addr"`
	Source        string  `json:"source"`
}

// List — GET /v1/pairs/rwa
//
// Returns the catalog of enabled RWA assets: orderbook pairs (`market:
// "secondary"`) unioned with primary-issuance launches (`market: "primary"`),
// so a client discovers every `{base}-{quote}` symbol the /v1/rwa endpoints
// serve. Empty database returns `[]`, not 404.
//
// Union semantics mirror GET /v1/rwa: entries are keyed by (source, base,
// quote), and an asset that trades AND is still in issuance appears ONCE as
// `secondary` — the addresses a transaction-builder needs come from the pair.
// Order is (source, base, quote) ascending, same guarantee as before the union.
func (d RWAPairsDeps) List() gin.HandlerFunc {
	type request struct{}
	bind := func(_ *gin.Context) (request, error) {
		return request{}, nil
	}
	action := func(ctx context.Context, _ request) ([]rwaPairDTO, error) {
		pairs, err := d.Lookup.EnabledRWAPairs(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]rwaPairDTO, 0, len(pairs))
		seen := make(map[string]struct{}, len(pairs))
		for _, p := range pairs {
			dto := pairCatalogDTO(p)
			seen[dto.Source+"|"+dto.Symbol] = struct{}{}
			out = append(out, dto)
		}
		// Launch-only assets join with null quote/orderbook addresses. A failure
		// here degrades to pairs-only rather than failing the whole catalog —
		// same contract as the overview's enabledLaunches.
		for _, l := range d.enabledLaunches(ctx) {
			dto := launchCatalogDTO(l)
			if _, dup := seen[dto.Source+"|"+dto.Symbol]; dup {
				continue // both facets → the secondary row already covers it
			}
			out = append(out, dto)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Source != out[j].Source {
				return out[i].Source < out[j].Source
			}
			if out[i].Base != out[j].Base {
				return out[i].Base < out[j].Base
			}
			return out[i].Quote < out[j].Quote
		})
		return out, nil
	}
	return common.Wrap(bind, action)
}

func (d RWAPairsDeps) enabledLaunches(ctx context.Context) []prices.RWALaunch {
	if d.Launches == nil {
		return nil
	}
	launches, err := d.Launches.EnabledLaunches(ctx, d.Source)
	if err != nil {
		return nil
	}
	return launches
}

func pairCatalogDTO(p prices.RWAPair) rwaPairDTO {
	base := strings.ToLower(p.BaseSymbol)
	quote := strings.ToLower(p.QuoteSymbol)
	return rwaPairDTO{
		Symbol:        base + "-" + quote,
		Base:          base,
		Quote:         quote,
		Market:        marketSecondary,
		TokenAddr:     nilIfEmpty(p.TokenAddr),
		QuoteAddr:     nilIfEmpty(p.QuoteAddr),
		OrderbookAddr: nilIfEmpty(p.OrderbookAddr),
		Source:        string(p.Source),
	}
}

func launchCatalogDTO(l prices.RWALaunch) rwaPairDTO {
	base := strings.ToLower(l.BaseSymbol)
	quote := strings.ToLower(l.QuoteSymbol)
	return rwaPairDTO{
		Symbol: base + "-" + quote,
		Base:   base,
		Quote:  quote,
		Market: marketPrimary,
		// quote_addr / orderbook_addr stay null by design: a primary sale is not
		// settled through the orderbook escrow, its payment flow is a separate
		// domain the launchpad owns.
		TokenAddr: nilIfEmpty(l.TokenAddr),
		Source:    string(l.Source),
	}
}

// Compile-time sanity: real *repositories.LookupRepository satisfies RWAPairsLister.
var _ RWAPairsLister = (*repositories.LookupRepository)(nil)
