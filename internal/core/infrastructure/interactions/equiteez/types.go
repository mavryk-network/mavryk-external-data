package equiteez

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// FlexibleFloat unmarshals JSON numbers or quoted numeric strings (Hasura /
// bigint fields). Equiteez orderbook prices come back as JSON-quoted strings
// for big numbers (`"56250000"`) and as bare floats for small ones — this
// type tolerates both.
//
// Backed by decimal, not float64: on-chain amounts routinely exceed float64's
// 53-bit integer range and the wire format is exact, so the value must reach
// numeric(38,18) storage without a lossy float hop.
//
// Non-finite inputs ("NaN"/"Inf"/"Infinity" — Postgres numeric NaN and float8
// infinity, which Hasura emits as quoted strings) decode to zero with NonFinite
// set instead of erroring. Erroring fails json.Unmarshal for the WHOLE
// response: one poisoned field would blank every pair's live tick, and in the
// backfill the batch would never parse, so the keyset cursor could never
// advance past it. Zero is already skipped by the callers' positivity guards,
// so one bad field costs one side of one row. Any OTHER parse failure still
// errors the decode — the tolerance is deliberately narrow.
type FlexibleFloat struct {
	d         decimal.Decimal
	nonFinite bool
}

func (f *FlexibleFloat) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	f.d, f.nonFinite = decimal.Decimal{}, false
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		if str == "" {
			return nil
		}
		s = str
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		if isNonFinite(s) {
			f.nonFinite = true
			return nil
		}
		return fmt.Errorf("parse numeric value %q: %w", s, err)
	}
	f.d = d
	return nil
}

// isNonFinite matches the tokens Postgres/Hasura use for numeric NaN and
// float8 infinity, in any casing or sign spelling.
func isNonFinite(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimPrefix(strings.TrimPrefix(t, "+"), "-")
	return t == "nan" || t == "inf" || t == "infinity"
}

// Decimal returns the exact parsed value (zero for null/empty/non-finite).
func (f FlexibleFloat) Decimal() decimal.Decimal { return f.d }

// NonFinite reports that the upstream sent NaN/Inf and the value was
// substituted with zero. Callers must skip such values and count the drop.
func (f FlexibleFloat) NonFinite() bool { return f.nonFinite }

// FlexibleFloatFromDecimal builds a value directly (fixtures/tests).
func FlexibleFloatFromDecimal(d decimal.Decimal) FlexibleFloat { return FlexibleFloat{d: d} }

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

// QuoteTokenAddress returns the on-chain contract address of the orderbook's
// quote token (the first row of `currencies`, same selection as QuoteSymbol);
// empty when the indexer reported no currency rows or no nested token.
func (o *EquiteezOrderbook) QuoteTokenAddress() string {
	if o == nil || len(o.Currencies) == 0 || o.Currencies[0].Token == nil {
		return ""
	}
	return o.Currencies[0].Token.Address
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
// OrderCursor is the keyset position of a forward walk over filled orders: the
// (ended_at, id) of the last row already ingested. A zero EndedAt means "no
// cursor yet" — the caller starts from its configured floor.
//
// Fill time leads and id is only the tie-break for orders that filled in the
// same instant. Ordering by id alone is unsafe: id is assigned at order
// CREATION, so a resting limit order that fills long after the cursor passed its
// id would never be returned.
type OrderCursor struct {
	EndedAt time.Time
	ID      int64
}

// Set reports whether the cursor holds a real position.
func (c OrderCursor) Set() bool { return !c.EndedAt.IsZero() }

type OrderbookOrder struct {
	ID               int64         `json:"id"`
	OrderType        int           `json:"order_type"`
	PricePerRWAToken FlexibleFloat `json:"price_per_rwa_token"`
	FulfilledAmount  FlexibleFloat `json:"fulfilled_amount"`
	EndedAt          string        `json:"ended_at"`
	OperationHash    string        `json:"operation_hash"`
}
