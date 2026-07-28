package equiteez

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"quotes/internal/core/infrastructure/graphql"
)

// FlexBig keeps a Hasura bigint/numeric verbatim as the string it arrived as
// (JSON number OR quoted string). Launchpad amount columns hold raw on-chain
// nats: supply-scale values of an 18-decimal token overflow float64/int64, so
// they must never round-trip through a numeric Go type before we hand them to
// decimal/big.Float.
type FlexBig string

func (f *FlexBig) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexBig(strings.TrimSpace(s))
		return nil
	}
	// Raw JSON number token kept verbatim — no float64 round-trip.
	*f = FlexBig(strings.TrimSpace(string(b)))
	return nil
}

func (f FlexBig) String() string { return string(f) }

// FlexTime unmarshals a Hasura timestamptz (possibly null) leniently: a null,
// empty, or unparseable value yields nil rather than failing the query — one
// malformed timestamp must not drop the whole launch.
type FlexTime struct{ T *time.Time }

func (f *FlexTime) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		f.T = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		f.T = nil
		return nil
	}
	if s = strings.TrimSpace(s); s == "" {
		f.T = nil
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		f.T = nil
		return nil
	}
	u := t.UTC()
	f.T = &u
	return nil
}

// Ptr returns the parsed time (or nil).
func (f FlexTime) Ptr() *time.Time { return f.T }

// Value returns the parsed time, or the zero time when absent (for sorting).
func (f FlexTime) Value() time.Time {
	if f.T == nil {
		return time.Time{}
	}
	return *f.T
}

// LaunchTokenRef is the nested `token { address token_id }` reference.
type LaunchTokenRef struct {
	Address string `json:"address"`
	TokenID int    `json:"token_id"`
}

// LaunchPaymentRow is one accepted payment currency for a sale option. Name is
// the human currency label ("USDT"); Price is raw in that token's units.
type LaunchPaymentRow struct {
	Name  string          `json:"name"`
	Price FlexBig         `json:"price"`
	Token *LaunchTokenRef `json:"token"`
}

// LaunchSaleOptionRow is one sale tier-bucket ("Starter", "Pinnacle", …). Each
// carries its own price ladder — the tiers are volume discounts off the base.
type LaunchSaleOptionRow struct {
	Name         string             `json:"name"`
	TotalBought  FlexBig            `json:"total_bought"`
	MaxAmountCap FlexBig            `json:"max_amount_cap"`
	IsPaused     bool               `json:"is_paused"`
	Payments     []LaunchPaymentRow `json:"payments"`
}

// LaunchRow is a launchpad_launch row with its sale options nested.
type LaunchRow struct {
	ID           int                   `json:"id"`
	Name         string                `json:"name"`
	Status       int                   `json:"status"`
	MaxAmountCap FlexBig               `json:"max_amount_cap"`
	TotalBought  FlexBig               `json:"total_bought"`
	SaleStart    FlexTime              `json:"sale_start"`
	SaleEnd      FlexTime              `json:"sale_end"`
	SaleClosed   FlexTime              `json:"sale_closed"`
	IsPaused     bool                  `json:"is_paused"`
	UpdatedAt    FlexTime              `json:"updated_at"`
	Token        *LaunchTokenRef       `json:"token"`
	SaleOptions  []LaunchSaleOptionRow `json:"sale_options"`
}

// BaseTierPrice returns the launch's undiscounted list price — the highest
// price across every sale option's payments — together with its currency label.
//
// The sale options are a volume-discount ladder (for KHBE: Starter 100 → …→
// Pinnacle 75 USDT), so the maximum is the base tier a buyer pays with no
// allocation. Picking by price rather than by option name keeps this working if
// the tiers are ever renamed. ok=false when the launch quotes no usable price.
func (r LaunchRow) BaseTierPrice() (raw string, currency string, ok bool) {
	var best *big.Float
	for _, so := range r.SaleOptions {
		for _, p := range so.Payments {
			v, parsed := new(big.Float).SetPrec(256).SetString(strings.TrimSpace(p.Price.String()))
			if !parsed || v.Sign() <= 0 {
				continue
			}
			if best == nil || v.Cmp(best) > 0 {
				best = v
				raw = strings.TrimSpace(p.Price.String())
				currency = strings.TrimSpace(p.Name)
			}
		}
	}
	return raw, currency, best != nil
}

// GetLaunchesByTokens returns every launchpad_launch attached to the given RWA
// token addresses, newest first, with sale options → payments nested in one
// round-trip. A token may have several launches; the caller selects which one to
// surface (see prices.SelectLaunch).
//
// NOTE: `token.in_allowlist` is deliberately NOT filtered here. The allowlist
// gates secondary-market trading, while a token in primary issuance typically
// has an active launch before it is allowlisted for an orderbook — filtering
// would hide exactly the assets this query exists to find. The caller supplies
// the address set, so scope is already bounded.
func (c *Client) GetLaunchesByTokens(ctx context.Context, addresses []string) ([]LaunchRow, error) {
	if len(addresses) == 0 {
		return nil, nil
	}
	const query = `
		query GetLaunchesByTokens($assets: [String!]) {
			launchpad_launch(
				where: { token: { address: { _in: $assets } } }
				order_by: { updated_at: desc }
			) {
				id
				name
				status
				max_amount_cap
				total_bought
				sale_start
				sale_end
				sale_closed
				is_paused
				updated_at
				token { address token_id }
				sale_options(order_by: { id: asc }) {
					name
					total_bought
					max_amount_cap
					is_paused
					payments(order_by: { id: asc }) {
						name
						price
						token { address token_id }
					}
				}
			}
		}
	`
	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query,
		map[string]interface{}{"assets": addresses}, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Rows []LaunchRow `json:"launchpad_launch"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal launchpad launches: %w", err)
	}
	return result.Rows, nil
}
