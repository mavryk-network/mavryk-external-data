// Package tickers defines the domain types for CoinGecko per-exchange ticker
// data (one row per (token, source, exchange, target_symbol, ts)).
//
// Ticker is the long-format row that lives in token_tickers; Snapshot is the
// transposed read-model used by /v1/tickers/:token/latest. Exchange is the
// lookup row for the exchanges table.
//
// Naming mirrors prices.PricePoint / prices.Snapshot so callers move between
// FT/RWA/Ticker domains with minimal cognitive overhead.
package tickers

import (
	"fmt"
	"strings"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// ExchangeKind classifies an exchange. CG /tickers does not tag CEX vs DEX,
// so we carry a hard-coded allowlist of well-known DEX identifiers in
// exchange_kind.go; everything else is 'cex'. Trivial to extend.
type ExchangeKind string

const (
	ExchangeKindCEX ExchangeKind = "cex"
	ExchangeKindDEX ExchangeKind = "dex"
)

// Exchange is one row in the exchanges lookup table.
type Exchange struct {
	ID                  string       // CG market.identifier (PK)
	Name                string       // CG market.name (human label)
	LogoURL             string       // CG market.logo (Pro flag), may be empty
	Kind                ExchangeKind // 'cex' default
	HasTradingIncentive bool         // CG market.has_trading_incentive
	LastSeenAt          time.Time    // updated on every observation
}

// Ticker is one row in the token_tickers hypertable. All numbers are native
// units: LastPrice is in TargetSymbol units, VolumeBase is in the token (e.g.
// MVRK count). Read-side conversion to ?in=usd,eur,... happens via the FX
// PriceConverter at handler time (ADR-0013).
type Ticker struct {
	Token        prices.Token
	Source       prices.Source
	ExchangeID   string
	TargetSymbol string // upstream quote symbol (lowercased: "btc", "usdt", "eth")
	Timestamp    time.Time
	LastPrice    decimal.Decimal  // price of 1 Token in TargetSymbol
	VolumeBase   *decimal.Decimal // 24h volume in Token units; nil when upstream omits
	BidAskSpread *decimal.Decimal // percent; nil when missing
	TrustScore   string           // "green" / "yellow" / "red" / ""
	IsAnomaly    bool
	TradeURL     string
	LastTradedAt time.Time // upstream last_traded_at; zero when missing
}

// Snapshot is the transposed read-model for GET /v1/tickers/:token/latest.
// One row per (exchange, target) for one token, with the 1D change %
// already joined in.
type Snapshot struct {
	Token     prices.Token
	Source    prices.Source
	Timestamp time.Time // newest ts across all rows
	Rows      []SnapshotRow
}

// SnapshotRow is one (exchange, target) datapoint inside a Snapshot.
type SnapshotRow struct {
	Exchange     Exchange
	TargetSymbol string
	Timestamp    time.Time
	LastPrice    decimal.Decimal
	VolumeBase   *decimal.Decimal
	BidAskSpread *decimal.Decimal
	TrustScore   string
	IsAnomaly    bool
	IsStale      bool // derived: ts < now - server.ticker_stale_after
	TradeURL     string
	Change24hPct *decimal.Decimal // nil when no row exists at ts-24h
}

// GroupBy is the dimension over which /distribution aggregates volume. Closed
// set — invalid values are rejected at the handler boundary.
type GroupBy string

const (
	GroupByExchange GroupBy = "exchange"
	GroupByTarget   GroupBy = "target"
)

// NewGroupBy validates an input string. Empty / unknown returns an error.
func NewGroupBy(s string) (GroupBy, error) {
	g := GroupBy(strings.ToLower(strings.TrimSpace(s)))
	switch g {
	case GroupByExchange, GroupByTarget:
		return g, nil
	default:
		return "", fmt.Errorf("group_by must be 'exchange' or 'target', got %q", s)
	}
}

// String implements fmt.Stringer.
func (g GroupBy) String() string { return string(g) }

// Distribution is the result of /v1/tickers/:token/distribution. Total is the
// sum of VolumeBase across all returned rows; share_pct is derived per row.
// When Total is zero (cold start, all volumes nil) Rows is empty.
type Distribution struct {
	Token     prices.Token
	Source    prices.Source
	Timestamp time.Time
	GroupBy   GroupBy
	Total     decimal.Decimal // sum of Rows[].VolumeBase, in Token units
	Rows      []DistributionRow
}

// DistributionRow is one bar of the pie chart. Exactly one of Exchange.ID and
// TargetSymbol is meaningful depending on Distribution.GroupBy:
//   - GroupBy=exchange ⇒ Exchange populated (id+name+logo), TargetSymbol "".
//   - GroupBy=target   ⇒ TargetSymbol populated, Exchange zero-valued.
type DistributionRow struct {
	Exchange     Exchange
	TargetSymbol string
	VolumeBase   decimal.Decimal // sum within the group, in Token units
	SharePct     decimal.Decimal // VolumeBase / Distribution.Total * 100, 6dp
}
