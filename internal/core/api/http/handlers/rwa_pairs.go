package handlers

import (
	"context"
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
}

// rwaPairDTO is the on-wire shape of one catalog entry. Lowercased fields
// keep the contract self-consistent with `/v1/rwa/:symbol` parsing
// (parseRWASymbol lowercases on the way in).
type rwaPairDTO struct {
	Base   string `json:"base"`
	Quote  string `json:"quote"`
	Source string `json:"source"`
}

// List — GET /v1/pairs/rwa
//
// Returns the catalog of enabled RWA pairs. Clients use it to discover
// which `{base}-{quote}` symbols to request from `/v1/rwa/:symbol/latest`.
// Empty database returns `[]`, not 404.
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
		for _, p := range pairs {
			out = append(out, rwaPairDTO{
				Base:   strings.ToLower(p.BaseSymbol),
				Quote:  strings.ToLower(p.QuoteSymbol),
				Source: string(p.Source),
			})
		}
		return out, nil
	}
	return common.Wrap(bind, action)
}

// Compile-time sanity: real *repositories.LookupRepository satisfies RWAPairsLister.
var _ RWAPairsLister = (*repositories.LookupRepository)(nil)
