package equiteez

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// FlexibleFloat unmarshals JSON numbers or quoted numeric strings (Hasura /
// bigint fields). Equiteez orderbook prices come back as JSON-quoted strings
// for big numbers (`"56250000"`) and as bare floats for small ones — this
// type tolerates both.
type FlexibleFloat float64

func (f *FlexibleFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = FlexibleFloat(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = FlexibleFloat(v)
	return nil
}

func (f FlexibleFloat) Float64() float64 { return float64(f) }

// TokenQuoteToken is the nested token row under orderbook_currency.token.
type TokenQuoteToken struct {
	Address string `json:"address"`
	TokenID int    `json:"token_id"`
}

// OrderbookCurrency is one quote currency row for an orderbook
// (matches Hasura `orderbook_currency`).
type OrderbookCurrency struct {
	Token        *TokenQuoteToken `json:"token"`
	CurrencyName string           `json:"currency_name"`
}

// EquiteezOrderbook is one orderbook row nested under `token.orderbooks`
// (matches indexer `orderbook` table).
//
// ID is the indexer's internal integer identifier (Hasura `orderbook.id`).
// Cached on `rwa_pairs.equiteez_orderbook_id` so the backfill job can join
// against `orderbook_order` without resolving the address every batch.
type EquiteezOrderbook struct {
	ID               int                 `json:"id"`
	Address          string              `json:"address"`
	InAllowlist      bool                `json:"in_allowlist"`
	LastMatchedPrice FlexibleFloat       `json:"last_matched_price"`
	LowestSellPrice  FlexibleFloat       `json:"lowest_sell_price"`
	HighestBuyPrice  FlexibleFloat       `json:"highest_buy_price"`
	SellOrderFee     FlexibleFloat       `json:"sell_order_fee"`
	BuyOrderFee      FlexibleFloat       `json:"buy_order_fee"`
	Currencies       []OrderbookCurrency `json:"currencies"`
}

// QuoteSymbol returns the human-readable currency name for the orderbook
// (the first row of `currencies`); empty when not reported.
func (o *EquiteezOrderbook) QuoteSymbol() string {
	if o == nil || len(o.Currencies) == 0 {
		return ""
	}
	return o.Currencies[0].CurrencyName
}

// TokenWithOrderbooks is one token row from the GraphQL queries
// (`GetTokensWithOrderbooks` / `GetAllowlistedTokensAndOrderbooks`).
type TokenWithOrderbooks struct {
	Address       string              `json:"address"`
	TokenID       int                 `json:"token_id"`
	InAllowlist   bool                `json:"in_allowlist"`
	TokenMetadata json.RawMessage     `json:"token_metadata"`
	TokenStandard *int                `json:"token_standard,omitempty"`
	Metadata      json.RawMessage     `json:"metadata,omitempty"`
	Orderbooks    []EquiteezOrderbook `json:"orderbooks"`
}

// OrderbookOrder is one row from `orderbook_order` — the indexer's per-order
// event log. Used by the Equiteez backfill job to reconstruct historical
// `last`-side prices from filled orders.
//
// Numeric fields come back as JSON-quoted strings for bigint columns and as
// bare numbers for ints; FlexibleFloat normalizes both. Timestamps are RFC3339
// (Hasura default).
type OrderbookOrder struct {
	ID               int64         `json:"id"`
	OrderType        int           `json:"order_type"`
	PricePerRWAToken FlexibleFloat `json:"price_per_rwa_token"`
	FulfilledAmount  FlexibleFloat `json:"fulfilled_amount"`
	EndedAt          string        `json:"ended_at"`
	OperationHash    string        `json:"operation_hash"`
}
