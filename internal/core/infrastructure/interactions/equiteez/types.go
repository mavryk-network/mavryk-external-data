package equiteez

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// FlexibleFloat unmarshals a JSON number or a quoted numeric string: Hasura
// sends bigint fields quoted (`"56250000"`) and small ones bare. Backed by
// decimal, not float64, since on-chain amounts exceed float64's 53-bit exact
// range and must reach numeric(38,18) storage intact.
//
// Non-finite inputs ("NaN"/"Inf"/"Infinity", which Postgres/Hasura emit as
// quoted strings) decode to zero with NonFinite set rather than erroring, since
// one poisoned field would fail json.Unmarshal for the whole response and stall
// the backfill cursor. Any other parse failure still errors.
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

// isNonFinite matches Postgres/Hasura NaN and infinity tokens, any casing or sign.
func isNonFinite(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimPrefix(strings.TrimPrefix(t, "+"), "-")
	return t == "nan" || t == "inf" || t == "infinity"
}

// Decimal returns the exact parsed value (zero for null/empty/non-finite).
func (f FlexibleFloat) Decimal() decimal.Decimal { return f.d }

// NonFinite reports that the upstream sent NaN/Inf and zero was substituted.
// Callers must skip such values and count the drop.
func (f FlexibleFloat) NonFinite() bool { return f.nonFinite }

// FlexibleFloatFromDecimal builds a value directly (fixtures/tests).
func FlexibleFloatFromDecimal(d decimal.Decimal) FlexibleFloat { return FlexibleFloat{d: d} }

// TokenQuoteToken is the nested token row under orderbook_currency.token.
type TokenQuoteToken struct {
	Address string `json:"address"`
	TokenID int    `json:"token_id"`
}

// OrderbookCurrency is one Hasura `orderbook_currency` row.
type OrderbookCurrency struct {
	Token        *TokenQuoteToken `json:"token"`
	CurrencyName string           `json:"currency_name"`
}

// EquiteezOrderbook is one indexer `orderbook` row nested under
// `token.orderbooks`. ID is cached on `rwa_pairs.equiteez_orderbook_id` so the
// backfill can join `orderbook_order` without re-resolving the address.
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

// QuoteSymbol returns the currency name from the first `currencies` row, or "".
func (o *EquiteezOrderbook) QuoteSymbol() string {
	if o == nil || len(o.Currencies) == 0 {
		return ""
	}
	return o.Currencies[0].CurrencyName
}

// QuoteTokenAddress returns the quote token's contract address from the first
// `currencies` row, or "" when the indexer reported none.
func (o *EquiteezOrderbook) QuoteTokenAddress() string {
	if o == nil || len(o.Currencies) == 0 || o.Currencies[0].Token == nil {
		return ""
	}
	return o.Currencies[0].Token.Address
}

// TokenWithOrderbooks is one token row from the GraphQL token queries.
type TokenWithOrderbooks struct {
	Address       string              `json:"address"`
	TokenID       int                 `json:"token_id"`
	InAllowlist   bool                `json:"in_allowlist"`
	TokenMetadata json.RawMessage     `json:"token_metadata"`
	TokenStandard *int                `json:"token_standard,omitempty"`
	Metadata      json.RawMessage     `json:"metadata,omitempty"`
	Orderbooks    []EquiteezOrderbook `json:"orderbooks"`
}

// OrderCursor is the keyset position of a forward walk over filled orders: the
// (ended_at, id) of the last ingested row, a zero EndedAt meaning "no cursor
// yet". Fill time leads and id only breaks ties — ordering by id alone is
// unsafe because id is assigned at order CREATION, so a resting order filling
// after the cursor passed its id would never be returned.
type OrderCursor struct {
	EndedAt time.Time
	ID      int64
}

// Set reports whether the cursor holds a real position.
func (c OrderCursor) Set() bool { return !c.EndedAt.IsZero() }

// OrderbookOrder is one `orderbook_order` row from the indexer's per-order
// event log; the backfill reconstructs historical `last` prices from filled
// ones. EndedAt is RFC3339 (Hasura default).
type OrderbookOrder struct {
	ID               int64         `json:"id"`
	OrderType        int           `json:"order_type"`
	PricePerRWAToken FlexibleFloat `json:"price_per_rwa_token"`
	FulfilledAmount  FlexibleFloat `json:"fulfilled_amount"`
	EndedAt          string        `json:"ended_at"`
	OperationHash    string        `json:"operation_hash"`
}
