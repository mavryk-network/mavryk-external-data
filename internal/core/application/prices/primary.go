package prices

import (
	"context"
	"errors"
	"time"

	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

const PrimaryBatchLimit = 256

const (
	PrimaryTransfer = "transfer"
	PrimaryHoldings = "holdings"
)

func ValidPrimaryMode(mode string) bool {
	return mode == "" || mode == PrimaryTransfer || mode == PrimaryHoldings
}

// PrimaryRequest identifies a block and valuation instant. Empty mode preserves
// pre-block transfer semantics; holdings mode uses the block's final state.
type PrimaryRequest struct {
	Network  string    `json:"network"`
	Contract string    `json:"contract"`
	TokenID  string    `json:"token_id"`
	At       time.Time `json:"at"`
	Level    int64     `json:"level"`
	Mode     string    `json:"mode,omitempty"`
}

type PrimaryTerms struct {
	Launchpad       string     `json:"launchpad"`
	Launch          string     `json:"launch"`
	SaleOption      string     `json:"sale_option"`
	PaymentContract string     `json:"payment_contract"`
	PaymentTokenID  string     `json:"payment_token_id"`
	PaymentDecimals int        `json:"payment_decimals"`
	Price           string     `json:"price"`
	ValidFrom       time.Time  `json:"valid_from"`
	ValidFromLevel  int64      `json:"valid_from_level"`
	ValidUntil      *time.Time `json:"valid_until,omitempty"`
	ValidUntilLevel *int64     `json:"valid_until_level,omitempty"`
	CoverageLevel   int64      `json:"coverage_level"`
	CoverageAt      time.Time  `json:"coverage_at"`
	Source          string     `json:"source"`
}

type PrimaryResult struct {
	PrimaryRequest
	USD    *string       `json:"usd"`
	Reason string        `json:"reason,omitempty"`
	Terms  *PrimaryTerms `json:"terms,omitempty"`
	FXRate *string       `json:"fx_rate,omitempty"`
	FXAt   *time.Time    `json:"fx_at,omitempty"`
}

type PrimaryUnavailable struct{ Reason string }

func (e *PrimaryUnavailable) Error() string { return "primary valuation: " + e.Reason }
func PrimaryMissing(reason string) error    { return &PrimaryUnavailable{Reason: reason} }

type PrimaryHistorySource interface {
	Terms(context.Context, PrimaryRequest) (PrimaryTerms, error)
}

type PrimaryValuator struct {
	History   PrimaryHistorySource
	Converter PriceConverter
}

// Evaluate preserves request order and makes every missing input explicit.
// A fixed Starter price still needs USDT/USD at the transfer's own instant.
func (s *PrimaryValuator) Evaluate(ctx context.Context, requests []PrimaryRequest) []PrimaryResult {
	out := make([]PrimaryResult, len(requests))
	for i, req := range requests {
		out[i].PrimaryRequest = req
		if !ValidPrimaryMode(req.Mode) {
			out[i].Reason = "mode_unsupported"
			continue
		}
		if s == nil || s.History == nil {
			out[i].Reason = "history_unavailable"
			continue
		}
		terms, err := s.History.Terms(ctx, req)
		if err != nil {
			out[i].Reason = "history_unavailable"
			var unavailable *PrimaryUnavailable
			if errors.As(err, &unavailable) {
				out[i].Reason = unavailable.Reason
			}
			continue
		}
		out[i].Terms = &terms
		price, err := decimal.NewFromString(terms.Price)
		if err != nil || !price.IsPositive() {
			out[i].Reason = "invalid_price"
			continue
		}
		if s.Converter == nil {
			out[i].Reason = "fx_missing"
			continue
		}
		fx, err := s.Converter.Convert(ctx, prices.Token("usdt"), prices.Currency("usd"), price, req.At)
		if err != nil {
			out[i].Reason = "fx_missing"
			continue
		}
		if fx.Stale || fx.RateTS.After(req.At) || !fx.Rate.IsPositive() || !fx.Amount.IsPositive() {
			out[i].Reason = "fx_stale"
			continue
		}
		usd, rate, at := fx.Amount.String(), fx.Rate.String(), fx.RateTS
		out[i].USD, out[i].FXRate, out[i].FXAt = &usd, &rate, &at
	}
	return out
}
