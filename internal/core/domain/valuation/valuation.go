package valuation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

const PolicyVersion = "treasury-excluded-v1"

type SupplyIdentity struct {
	Network  string `json:"network"`
	Contract string `json:"contract"`
	TokenID  string `json:"token_id"`
}

func (i SupplyIdentity) Valid() bool {
	return i.Network != "" && i.Contract != "" && ValidSupplyNat(i.TokenID, 256)
}
func (i SupplyIdentity) Key() string { return i.Network + ":" + i.Contract + ":" + i.TokenID }

type SupplyBlock struct {
	Level     int64     `json:"level"`
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
}

type Quality struct {
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	FreshUntil *time.Time `json:"fresh_until,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func Missing(reason string) Quality { return Quality{Status: "unavailable", Reason: reason} }

// At only degrades an observation. HTTP fetches never create freshness.
func (q Quality) At(now time.Time) Quality {
	if q.Status == "unavailable" {
		return q
	}
	if q.ExpiresAt == nil || !now.Before(*q.ExpiresAt) {
		q.Status, q.Reason = "unavailable", "observation_expired"
		return q
	}
	if q.Status == "fresh" && (q.FreshUntil == nil || now.After(*q.FreshUntil)) {
		q.Status, q.Reason = "stale", "observation_expired"
	}
	return q
}

func Combine(a, b Quality) Quality {
	if a.Status == "unavailable" {
		return a
	}
	if b.Status == "unavailable" {
		return b
	}
	q := a
	if b.Status == "stale" {
		q.Status, q.Reason = b.Status, b.Reason
	}
	if q.FreshUntil == nil || b.FreshUntil != nil && b.FreshUntil.Before(*q.FreshUntil) {
		q.FreshUntil = b.FreshUntil
	}
	if q.ExpiresAt == nil || b.ExpiresAt != nil && b.ExpiresAt.Before(*q.ExpiresAt) {
		q.ExpiresAt = b.ExpiresAt
	}
	return q
}

// Policy carries the existing backend SupplyPolicy budgets, now owned here.
type Policy struct{ Fresh, PriceFresh, PrimaryPriceFresh, Retention time.Duration }

func (p Policy) Defaulted() Policy {
	if p.Fresh <= 0 {
		p.Fresh = 900 * time.Second
	}
	if p.PriceFresh <= 0 {
		p.PriceFresh = 900 * time.Second
	}
	if p.PrimaryPriceFresh <= 0 {
		p.PrimaryPriceFresh = 75 * time.Minute
	}
	if p.Retention <= 0 {
		p.Retention = 24 * time.Hour
	}
	return p
}

func (p Policy) Quality(at, now time.Time, basis string) Quality {
	p = p.Defaulted()
	if at.IsZero() || at.After(now) {
		return Missing("invalid_timestamp")
	}
	fresh := p.Fresh
	if basis == "last" {
		fresh = p.PriceFresh
	}
	if basis == "primary_list_price" {
		fresh = p.PrimaryPriceFresh
	}
	f, e := at.Add(fresh), at.Add(p.Retention)
	return (Quality{Status: "fresh", FreshUntil: &f, ExpiresAt: &e}).At(now)
}

type Metadata struct {
	Decimals         *int         `json:"decimals"`
	ExpectedDecimals *int         `json:"expected_decimals,omitempty"`
	Version          string       `json:"version,omitempty"`
	Source           string       `json:"source,omitempty"`
	Block            *SupplyBlock `json:"block,omitempty"`
	Reason           string       `json:"reason,omitempty"`
}

type Supply struct {
	PolicyVersion      string         `json:"policy_version,omitempty"`
	Identity           SupplyIdentity `json:"identity"`
	Block              SupplyBlock    `json:"block"`
	IndexedHead        SupplyBlock    `json:"indexed_head"`
	ObservedAt         time.Time      `json:"observed_at"`
	Total              string         `json:"total_supply,omitempty"`
	Excluded           string         `json:"excluded_supply,omitempty"`
	Circulating        string         `json:"circulating_supply,omitempty"`
	Metadata           Metadata       `json:"metadata"`
	TotalQuality       Quality        `json:"total_quality"`
	CirculatingQuality Quality        `json:"circulating_quality"`
	ScopeVersion       string         `json:"scope_version"`
	ScopeFetchedAt     time.Time      `json:"scope_fetched_at"`
	ScopeStatus        string         `json:"scope_status"`
	Source             string         `json:"source"`
	Provenance         string         `json:"provenance"`
}

type Quote struct {
	Contract string `json:"contract"`
	TokenID  string `json:"token_id"`
	Symbol   string `json:"symbol"`
}

type Conversion struct {
	MarketCap        *decimal.Decimal `json:"market_cap"`
	FDV              *decimal.Decimal `json:"fdv"`
	Rate             *decimal.Decimal `json:"rate"`
	FXAt             *time.Time       `json:"fx_at"`
	MarketCapQuality Quality          `json:"market_cap_quality"`
	FDVQuality       Quality          `json:"fdv_quality"`
}

type Point struct {
	Symbol           string                `json:"symbol"`
	Quote            Quote                 `json:"quote"`
	PairID           int64                 `json:"pair_id,omitempty"`
	Orderbook        string                `json:"orderbook,omitempty"`
	Launchpad        string                `json:"launchpad,omitempty"`
	SaleStatus       string                `json:"sale_status,omitempty"`
	SalePaused       *bool                 `json:"sale_paused,omitempty"`
	Generation       string                `json:"generation,omitempty"`
	AsOf             time.Time             `json:"as_of"`
	PriceAt          time.Time             `json:"price_at"`
	LastTradeAt      *time.Time            `json:"last_trade_at,omitempty"`
	PriceBasis       string                `json:"price_basis"`
	Price            *decimal.Decimal      `json:"price"`
	PriceQuality     Quality               `json:"price_quality"`
	MarketCap        *decimal.Decimal      `json:"market_cap"`
	FDV              *decimal.Decimal      `json:"fdv"`
	MarketCapQuality Quality               `json:"market_cap_quality"`
	FDVQuality       Quality               `json:"fdv_quality"`
	Conversions      map[string]Conversion `json:"conversions,omitempty"`
}

type Snapshot struct {
	CurrentOnly   bool           `json:"-"`
	SchemaVersion int            `json:"schema_version"`
	Revision      string         `json:"revision"`
	PolicyVersion string         `json:"policy_version"`
	Identity      SupplyIdentity `json:"identity"`
	Supply        Supply         `json:"supply"`
	AsOf          time.Time      `json:"as_of"`
	Valuations    []Point        `json:"valuations"`
	Invalidated   bool           `json:"invalidated,omitempty"`
	Reason        string         `json:"reason,omitempty"`
}

func Fingerprint(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Money is the backend SupplyMoney algorithm without its legacy float boundary.
func Money(raw string, scale int, price decimal.Decimal) (*decimal.Decimal, error) {
	if !ValidSupplyNat(raw, 1024) || scale < 0 || scale > 40 || !price.IsPositive() {
		return nil, fmt.Errorf("invalid valuation inputs")
	}
	amount, err := decimal.NewFromString(raw)
	if err != nil {
		return nil, err
	}
	v := amount.Shift(-int32(scale)).Mul(price)
	return &v, nil
}

func Calculate(s Supply, p Point, policy Policy) Point {
	p.MarketCap, p.FDV = nil, nil
	p.MarketCapQuality, p.FDVQuality = Missing("price_missing"), Missing("price_missing")
	p.PriceQuality = Missing("price_missing")
	if p.Price == nil || !p.Price.IsPositive() {
		return p
	}
	p.PriceQuality = policy.Quality(p.PriceAt, p.AsOf, p.PriceBasis)
	if s.Metadata.Decimals == nil || s.Metadata.Reason != "" {
		reason := s.Metadata.Reason
		if reason == "" {
			reason = "decimals_unknown"
		}
		p.MarketCapQuality, p.FDVQuality = Missing(reason), Missing(reason)
		return p
	}
	pq := p.PriceQuality
	p.FDVQuality = Combine(s.TotalQuality, pq)
	p.MarketCapQuality = Combine(s.CirculatingQuality, pq)
	if p.FDVQuality.Status != "unavailable" {
		var err error
		p.FDV, err = Money(s.Total, *s.Metadata.Decimals, *p.Price)
		if err != nil {
			p.FDVQuality = Missing("invalid_supply")
		}
	}
	if p.MarketCapQuality.Status != "unavailable" {
		var err error
		p.MarketCap, err = Money(s.Circulating, *s.Metadata.Decimals, *p.Price)
		if err != nil {
			p.MarketCapQuality = Missing("invalid_supply")
		}
	}
	return p
}

// At projects a copy for latest only; chart quality is evaluated at its as_of.
func (s Snapshot) At(now time.Time) Snapshot {
	s.Supply.TotalQuality = s.Supply.TotalQuality.At(now)
	s.Supply.CirculatingQuality = s.Supply.CirculatingQuality.At(now)
	s.Valuations = append([]Point(nil), s.Valuations...)
	for i := range s.Valuations {
		p := &s.Valuations[i]
		p.PriceQuality = p.PriceQuality.At(now)
		p.MarketCapQuality, p.FDVQuality = p.MarketCapQuality.At(now), p.FDVQuality.At(now)
		if p.MarketCapQuality.Status == "unavailable" {
			p.MarketCap = nil
		}
		if p.FDVQuality.Status == "unavailable" {
			p.FDV = nil
		}
		conv := make(map[string]Conversion, len(p.Conversions))
		for k, v := range p.Conversions {
			v.MarketCapQuality, v.FDVQuality = v.MarketCapQuality.At(now), v.FDVQuality.At(now)
			if v.MarketCapQuality.Status == "unavailable" {
				v.MarketCap = nil
			}
			if v.FDVQuality.Status == "unavailable" {
				v.FDV = nil
			}
			conv[k] = v
		}
		p.Conversions = conv
	}
	return s
}
