package equiteez

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// FlexibleFloat unmarshals JSON numbers or quoted numeric strings (Hasura / bigint fields).
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

// OrderbookCurrency is one quote currency row for an orderbook (matches Hasura orderbook_currency).
type OrderbookCurrency struct {
	Token        *TokenQuoteToken `json:"token"`
	CurrencyName string           `json:"currency_name"`
}

// EquiteezOrderbook is one orderbook row nested under token.orderbooks (matches indexer orderbook table).
type EquiteezOrderbook struct {
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

// TokenWithOrderbooks is one token row from the tokensWithOrderbooks GraphQL query.
type TokenWithOrderbooks struct {
	Address       string              `json:"address"`
	TokenID       int                 `json:"token_id"`
	InAllowlist   bool                `json:"in_allowlist"`
	TokenMetadata json.RawMessage     `json:"token_metadata"`
	TokenStandard *int                `json:"token_standard,omitempty"`
	Metadata      json.RawMessage     `json:"metadata,omitempty"`
	Orderbooks    []EquiteezOrderbook `json:"orderbooks"`
}

// FirstOrderbook returns the first orderbook if any (same selection as previous []interface{}[0]).
func (t *TokenWithOrderbooks) FirstOrderbook() *EquiteezOrderbook {
	if t == nil || len(t.Orderbooks) == 0 {
		return nil
	}
	return &t.Orderbooks[0]
}

// FlexInt64 unmarshals JSON int, float, or string for bigint-ish GraphQL scalars.
type FlexInt64 int64

func (f *FlexInt64) UnmarshalJSON(b []byte) error {
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
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		*f = FlexInt64(v)
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = FlexInt64(v)
	return nil
}

func (f FlexInt64) Int64() int64 { return int64(f) }

// RWATransfer is one row from the GetRWATransfers GraphQL query (equiteez_user_token_transfer–style fields).
type RWATransfer struct {
	ID        FlexInt64 `json:"id"`
	Hash      string    `json:"hash"`
	Type      string    `json:"type"`
	Level     FlexInt64 `json:"level"`
	Timestamp string    `json:"timestamp"`
	Sender    string    `json:"sender"`
	Target    string    `json:"target"`
	Amount    FlexInt64 `json:"amount"`
	TokenID   int       `json:"tokenId"`
	Contract  string    `json:"contract"`
}
