package prices

import (
	"context"
	"strconv"
	"strings"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

type RWASnapshotReader interface {
	Snapshot(context.Context, int64) (prices.RWAMarketSnapshot, bool, error)
}

// PricingQuote distinguishes native-price availability from FX availability.
// Money is an exact decimal string; null is never a manufactured zero.
type PricingQuote struct {
	Method      string     `json:"method"`
	Status      string     `json:"status"`
	Reason      *string    `json:"reason"`
	Price       *string    `json:"price"`
	USD         *string    `json:"usd"`
	NativeQuote string     `json:"native_quote"`
	AsOf        *time.Time `json:"as_of"`
	ValidUntil  *time.Time `json:"valid_until"`
	FXStatus    string     `json:"fx_status"`
	FXAt        *time.Time `json:"fx_at"`
}

type LastSaleQuote struct {
	PricingQuote
	ExecutedAt *time.Time `json:"executed_at"`
}

type PrimaryIssuanceQuote struct {
	PricingQuote
	LaunchID     int    `json:"launch_id"`
	LaunchStatus string `json:"launch_status"`
	Active       bool   `json:"active"`
}

type RWAPricing struct {
	Symbol           string                `json:"symbol"`
	Network          string                `json:"network"`
	Source           string                `json:"source"`
	TokenAddress     string                `json:"token_address"`
	TokenID          *string               `json:"token_id"`
	QuoteAddress     string                `json:"quote_address"`
	QuoteTokenID     *string               `json:"quote_token_id"`
	OrderbookAddress *string               `json:"orderbook_address"`
	ObservedAt       *time.Time            `json:"observed_at"`
	ValidUntil       *time.Time            `json:"valid_until"`
	MarketPrice      PricingQuote          `json:"market_price"`
	LastSale         LastSaleQuote         `json:"last_sale"`
	PrimaryIssuance  *PrimaryIssuanceQuote `json:"primary_issuance"`
}

type RWAPricingService struct {
	Snapshots RWASnapshotReader
	Converter PriceConverter
	Network   string
	MaxAge    time.Duration
	Now       func() time.Time
}

func unavailableQuote(method, native, reason string) PricingQuote {
	return PricingQuote{Method: method, NativeQuote: strings.ToLower(native),
		Status: "unavailable", Reason: &reason, FXStatus: "not_applicable"}
}

func idString(id *int) *string {
	if id == nil {
		return nil
	}
	s := strconv.Itoa(*id)
	return &s
}

func (s *RWAPricingService) clock() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *RWAPricingService) maxAge() time.Duration {
	if s.MaxAge <= 0 {
		return 180 * time.Second
	}
	return s.MaxAge
}

// Get uses complete observations; it never joins independent latest bid/ask
// points, never falls back to last, and never mutates legacy price semantics.
func (s *RWAPricingService) Get(ctx context.Context, pair *prices.RWAPair, launch *prices.RWALaunch) (RWAPricing, error) {
	now := s.clock()
	out := RWAPricing{Network: s.Network, Source: string(prices.SourceEquiteez)}
	if pair != nil {
		out.Symbol = strings.ToLower(pair.BaseSymbol + "-" + pair.QuoteSymbol)
		out.TokenAddress, out.QuoteAddress = pair.TokenAddr, pair.QuoteAddr
		out.OrderbookAddress = &pair.OrderbookAddr
		out.MarketPrice = unavailableQuote("orderbook_mid", pair.QuoteSymbol, "snapshot_missing")
		out.LastSale.PricingQuote = unavailableQuote("last_trade", pair.QuoteSymbol, "snapshot_missing")
		if s.Snapshots != nil {
			snapshot, found, err := s.Snapshots.Snapshot(ctx, pair.ID)
			if err != nil {
				return out, err
			}
			if found {
				s.applySnapshot(ctx, &out, *pair, snapshot, now)
			}
		}
	} else if launch != nil {
		out.Symbol = launch.Symbol()
		out.TokenAddress, out.QuoteAddress = launch.TokenAddr, launch.QuoteAddr
		out.TokenID, out.QuoteTokenID = idString(&launch.TokenID), idString(launch.PricingQuoteTokenID)
		out.MarketPrice = unavailableQuote("orderbook_mid", launch.QuoteSymbol, "primary_market")
		out.LastSale.PricingQuote = unavailableQuote("last_trade", launch.QuoteSymbol, "primary_market")
	}
	if launch != nil {
		q := s.primary(ctx, *launch, now)
		// A reused ticker must not attach another token's issuance to this book.
		if pair != nil && (launch.TokenAddr != pair.TokenAddr || launch.QuoteAddr != pair.QuoteAddr ||
			!strings.EqualFold(launch.QuoteSymbol, pair.QuoteSymbol)) {
			q.PricingQuote = unavailableQuote("primary_list_price", launch.QuoteSymbol, "identity_mismatch")
		}
		out.PrimaryIssuance = &q
		// A newly created orderbook need not have a collected snapshot yet.
		// The matching, verified launch still supplies token-0 identity and a
		// usable primary quote; do not erase it behind null aggregate identity.
		if pair != nil && launch.TokenAddr == pair.TokenAddr && launch.QuoteAddr == pair.QuoteAddr &&
			strings.EqualFold(launch.QuoteSymbol, pair.QuoteSymbol) && launch.TokenID == 0 &&
			launch.PricingQuoteTokenID != nil && *launch.PricingQuoteTokenID == 0 {
			if out.TokenID == nil {
				out.TokenID = idString(&launch.TokenID)
			}
			if out.QuoteTokenID == nil {
				out.QuoteTokenID = idString(launch.PricingQuoteTokenID)
			}
		}
		if out.ObservedAt == nil {
			out.ObservedAt, out.ValidUntil = q.AsOf, q.ValidUntil
		}
	}
	return out, nil
}

func (s *RWAPricingService) applySnapshot(ctx context.Context, out *RWAPricing, pair prices.RWAPair, snap prices.RWAMarketSnapshot, now time.Time) {
	expires := snap.ObservedAt.Add(s.maxAge())
	out.ObservedAt, out.ValidUntil = &snap.ObservedAt, &expires
	out.TokenID, out.QuoteTokenID = idString(snap.TokenID), idString(snap.QuoteTokenID)
	reason := snap.Reason
	switch {
	case snap.Network != s.Network || snap.PairID != pair.ID || snap.TokenAddr != pair.TokenAddr ||
		snap.QuoteAddr != pair.QuoteAddr || snap.OrderbookAddr != pair.OrderbookAddr ||
		!strings.EqualFold(snap.NativeQuote, pair.QuoteSymbol):
		reason = "identity_mismatch"
	case snap.TokenID == nil || *snap.TokenID != 0 || snap.QuoteTokenID == nil || *snap.QuoteTokenID != 0:
		reason = "unsupported_identity"
	case snap.IndexerGeneration == "" || snap.IndexerGeneration != pair.IndexerGeneration:
		reason = "generation_mismatch"
	case snap.ObservedAt.IsZero() || snap.ObservedAt.After(now):
		reason = "invalid_observation_time"
	}
	if reason != "" {
		out.MarketPrice = unavailableQuote("orderbook_mid", pair.QuoteSymbol, reason)
		out.LastSale.PricingQuote = unavailableQuote("last_trade", pair.QuoteSymbol, reason)
		return
	}
	stale := !now.Before(expires)
	marketExpires := expires
	for _, expiry := range []*time.Time{snap.BidExpiresAt, snap.AskExpiresAt} {
		if expiry != nil && expiry.Before(marketExpires) {
			marketExpires = *expiry
		}
	}
	marketReason := ""
	switch {
	case stale:
		marketReason = "snapshot_stale"
	case !now.Before(marketExpires):
		marketReason = "order_expired"
	case snap.Bid == nil && snap.Ask == nil:
		marketReason = "empty_orderbook"
	case snap.Bid == nil || snap.Ask == nil:
		marketReason = "one_sided_orderbook"
	case !snap.Bid.IsPositive() || !snap.Ask.IsPositive():
		marketReason = "invalid_orderbook"
	case snap.Bid.GreaterThan(*snap.Ask):
		marketReason = "crossed_orderbook"
	}
	if marketReason == "" {
		mid := snap.Bid.Add(*snap.Ask).Mul(decimal.New(5, -1))
		out.MarketPrice = s.quote(ctx, "orderbook_mid", pair.QuoteSymbol, mid, snap.ObservedAt)
		out.MarketPrice.ValidUntil = &marketExpires
	} else {
		out.MarketPrice = unavailableQuote("orderbook_mid", pair.QuoteSymbol, marketReason)
		out.MarketPrice.AsOf, out.MarketPrice.ValidUntil = &snap.ObservedAt, &marketExpires
	}
	saleReason := ""
	switch {
	case snap.LastPrice == nil || !snap.LastPrice.IsPositive():
		saleReason = "no_trade"
	case snap.LastTradeAt == nil || snap.LastTradeAt.IsZero():
		saleReason = "execution_time_missing"
	case snap.LastTradeAt.After(snap.ObservedAt):
		saleReason = "invalid_execution_time"
	}
	if saleReason == "" {
		out.LastSale.PricingQuote = s.quote(ctx, "last_trade", pair.QuoteSymbol, *snap.LastPrice, *snap.LastTradeAt)
		out.LastSale.ExecutedAt, out.LastSale.ValidUntil = snap.LastTradeAt, &expires
		if stale {
			reason := "snapshot_stale"
			out.LastSale.Status, out.LastSale.Reason = "stale", &reason
		}
	} else {
		out.LastSale.PricingQuote = unavailableQuote("last_trade", pair.QuoteSymbol, saleReason)
	}
}

func (s *RWAPricingService) primary(ctx context.Context, l prices.RWALaunch, now time.Time) PrimaryIssuanceQuote {
	q := PrimaryIssuanceQuote{LaunchID: l.LaunchID, LaunchStatus: l.Status, Active: l.Active,
		PricingQuote: unavailableQuote("primary_list_price", l.QuoteSymbol, "primary_unavailable")}
	expires := l.LastSyncedAt.Add(s.maxAge())
	if l.SaleEnd != nil && l.SaleEnd.Before(expires) {
		expires = *l.SaleEnd
	}
	q.AsOf, q.ValidUntil = &l.LastSyncedAt, &expires
	reason := ""
	switch {
	case l.TokenID != 0 || l.TokenAddr == "" || l.QuoteAddr == "" || l.PricingQuoteTokenID == nil || *l.PricingQuoteTokenID != 0:
		reason = "unsupported_identity"
	case !l.Price.IsPositive():
		reason = "invalid_price"
	case l.LastSyncedAt.IsZero() || l.LastSyncedAt.After(now):
		reason = "invalid_observation_time"
	case !now.Before(expires):
		reason = "snapshot_stale"
	case !l.Active || l.Status != "active" || l.SaleClosed != nil || (l.SaleStart != nil && now.Before(*l.SaleStart)):
		reason = "sale_inactive"
	}
	if reason != "" {
		q.Reason = &reason
		return q
	}
	q.PricingQuote = s.quote(ctx, "primary_list_price", l.QuoteSymbol, l.Price, l.LastSyncedAt)
	q.ValidUntil = &expires
	return q
}

func (s *RWAPricingService) quote(ctx context.Context, method, native string, price decimal.Decimal, at time.Time) PricingQuote {
	p := price.String()
	q := PricingQuote{Method: method, Status: "available", Price: &p, NativeQuote: strings.ToLower(native), AsOf: &at, FXStatus: "missing"}
	if s.Converter == nil {
		return q
	}
	token, err := prices.NewToken(native)
	if err != nil {
		q.FXStatus = "unsupported"
		return q
	}
	fx, err := s.Converter.Convert(ctx, token, prices.Currency("usd"), price, at)
	if err != nil {
		return q
	}
	q.FXAt = &fx.RateTS
	if fx.Stale || fx.RateTS.IsZero() || fx.RateTS.After(at) || !fx.Rate.IsPositive() || !fx.Amount.IsPositive() {
		q.FXStatus = "stale"
		return q
	}
	usd := fx.Amount.String()
	q.USD, q.FXStatus = &usd, "available"
	return q
}
