// Package primary reads the durable on-chain launchLedger history. Polling a
// current quote, or changing its cache TTL, never creates historical evidence.
package primary

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"quotes/internal/config"
	app "quotes/internal/core/application/prices"
	"quotes/internal/core/infrastructure/interactions/mvkt"

	"golang.org/x/sync/singleflight"
)

const (
	historyPageSize   = 512
	historyRowLimit   = 8192
	launchKeyLimit    = 128
	historyCacheTTL   = 30 * time.Second
	historyFailureTTL = 3 * time.Second
)

const basenet = "basenet"
const basenetAllowlistURL = "https://eqtz-admin.equiteez.com/equiteez-contract-allowlist-basenet.json"

type History struct {
	client       *mvkt.Client
	allowlistURL string
	mu           sync.Mutex
	snapshot     *snapshot
	loadErr      error
	expires      time.Time
	flight       singleflight.Group
}

func NewHistory(cfg config.MvKTConfig, api *config.APIConfig) *History {
	return &History{client: mvkt.NewClient(cfg, api), allowlistURL: basenetAllowlistURL}
}

type head struct {
	Chain     string    `json:"chain"`
	Level     int64     `json:"level"`
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
}

type snapshot struct {
	head      head
	histories []launchHistory
}

type launchHistory struct {
	launchpad string
	key       string
	updates   []update
	payments  map[paymentIdentity]metadataHistory
}

type update struct {
	ID        int64         `json:"id"`
	Level     int64         `json:"level"`
	Timestamp time.Time     `json:"timestamp"`
	Action    string        `json:"action"`
	Value     *launchRecord `json:"value"`
}

// Only valuation inputs participate in equality. A purchase updating
// totalBought must not look like a price change in that purchase's block.
type launchRecord struct {
	TokenContract string                `json:"tokenContractAddress"`
	TokenID       string                `json:"tokenId"`
	Status        string                `json:"status"`
	IsPaused      bool                  `json:"isPaused"`
	SaleStart     *time.Time            `json:"saleStart"`
	SaleEnd       *time.Time            `json:"saleEnd"`
	SaleClosed    *time.Time            `json:"saleClosed"`
	Options       map[string]saleOption `json:"saleOptions"`
}

type saleOption struct {
	IsPaused  bool               `json:"isPaused"`
	SaleStart *time.Time         `json:"saleStart"`
	SaleEnd   *time.Time         `json:"saleEnd"`
	Payments  map[string]payment `json:"payments"`
}

type payment struct {
	Price    string `json:"price"`
	Currency struct {
		FA2 *struct {
			Contract string `json:"tokenContractAddress"`
			TokenID  string `json:"tokenId"`
		} `json:"fa2"`
	} `json:"currency"`
}

func (h *History) Terms(ctx context.Context, req app.PrimaryRequest) (app.PrimaryTerms, error) {
	if !app.ValidPrimaryMode(req.Mode) {
		return app.PrimaryTerms{}, app.PrimaryMissing("mode_unsupported")
	}
	if req.Network != basenet {
		return app.PrimaryTerms{}, app.PrimaryMissing("network_mismatch")
	}
	s, err := h.readSnapshot(ctx)
	if err != nil {
		return app.PrimaryTerms{}, err
	}
	if req.Level > s.head.Level || req.At.After(s.head.Timestamp) {
		return app.PrimaryTerms{}, app.PrimaryMissing("chain_coverage_missing")
	}
	var candidates []app.PrimaryTerms
	reason := "launch_missing"
	for _, history := range s.histories {
		terms, matches, err := h.at(history, req, s.head)
		if !matches {
			continue
		}
		if err != nil {
			// An overlapping launch with unknown conditions is ambiguous too.
			if len(candidates) > 0 {
				return app.PrimaryTerms{}, app.PrimaryMissing("launch_ambiguous")
			}
			if u, ok := err.(*app.PrimaryUnavailable); ok {
				reason = u.Reason
			}
			continue
		}
		if reason != "launch_missing" {
			return app.PrimaryTerms{}, app.PrimaryMissing("launch_ambiguous")
		}
		candidates = append(candidates, terms)
	}
	if len(candidates) == 0 {
		return app.PrimaryTerms{}, app.PrimaryMissing(reason)
	}
	if len(candidates) != 1 {
		return app.PrimaryTerms{}, app.PrimaryMissing("launch_ambiguous")
	}
	return candidates[0], nil
}

func signature(r *launchRecord) string { b, _ := json.Marshal(r); return string(b) }

func (h *History) at(history launchHistory, req app.PrimaryRequest, covered head) (app.PrimaryTerms, bool, error) {
	index := -1
	for i, u := range history.updates {
		if u.Level < req.Level || req.Mode == app.PrimaryHoldings && u.Level == req.Level {
			index = i
		}
	}
	if index < 0 {
		for _, u := range history.updates {
			if u.Level == req.Level && u.Value != nil && u.Value.TokenContract == req.Contract && u.Value.TokenID == req.TokenID {
				return app.PrimaryTerms{}, true, app.PrimaryMissing("same_block_change")
			}
		}
		return app.PrimaryTerms{}, false, nil
	}
	r := history.updates[index].Value
	if r == nil || r.TokenContract != req.Contract || r.TokenID != req.TokenID {
		return app.PrimaryTerms{}, false, nil
	}
	if history.updates[index].Timestamp.After(req.At) {
		return app.PrimaryTerms{}, true, app.PrimaryMissing("block_time_mismatch")
	}
	sig := signature(r)
	if req.Mode != app.PrimaryHoldings {
		for _, u := range history.updates {
			if u.Level == req.Level && (signature(u.Value) != sig || !u.Timestamp.Equal(req.At)) {
				return app.PrimaryTerms{}, true, app.PrimaryMissing("same_block_change")
			}
		}
	}
	if r.Status != "ACTIVE" || r.IsPaused || r.SaleClosed != nil || !inside(req.At, r.SaleStart, r.SaleEnd) {
		return app.PrimaryTerms{}, true, app.PrimaryMissing("launch_inactive")
	}
	option, found := r.Options["Starter"]
	if !found || len(r.Options) != 1 {
		return app.PrimaryTerms{}, true, app.PrimaryMissing("sale_option_unsupported")
	}
	if option.IsPaused || !inside(req.At, option.SaleStart, option.SaleEnd) {
		return app.PrimaryTerms{}, true, app.PrimaryMissing("launch_inactive")
	}
	pay, found := option.Payments["USDT"]
	if !found || len(option.Payments) != 1 || pay.Currency.FA2 == nil {
		return app.PrimaryTerms{}, true, app.PrimaryMissing("payment_unsupported")
	}
	identity := paymentIdentity{Contract: pay.Currency.FA2.Contract, TokenID: pay.Currency.FA2.TokenID}
	metadata, err := history.payments[identity].at(req)
	if err != nil {
		return app.PrimaryTerms{}, true, err
	}
	decimals := *metadata.scale
	price, ok := humanPrice(pay.Price, decimals)
	if !ok {
		return app.PrimaryTerms{}, true, app.PrimaryMissing("invalid_price")
	}
	start := index
	for start > 0 && signature(history.updates[start-1].Value) == sig {
		start--
	}
	terms := app.PrimaryTerms{
		Launchpad: history.launchpad, Launch: history.key, SaleOption: "Starter",
		PaymentContract: pay.Currency.FA2.Contract, PaymentTokenID: pay.Currency.FA2.TokenID,
		PaymentDecimals: decimals, Price: price,
		ValidFrom: history.updates[start].Timestamp, ValidFromLevel: history.updates[start].Level,
		CoverageLevel: covered.Level, CoverageAt: covered.Timestamp,
		Source: "mvkt_launch_ledger:onchain_token_metadata",
	}
	for i := index + 1; i < len(history.updates); i++ {
		u := history.updates[i]
		if signature(u.Value) != sig {
			terms.ValidUntil, terms.ValidUntilLevel = &u.Timestamp, &u.Level
			if !req.At.Before(u.Timestamp) && req.Level < u.Level {
				return app.PrimaryTerms{}, true, app.PrimaryMissing("block_time_mismatch")
			}
			break
		}
	}
	// Native price validity also depends on the effective token scale.
	if metadata.from.Level > terms.ValidFromLevel {
		terms.ValidFrom, terms.ValidFromLevel = metadata.from.Timestamp, metadata.from.Level
	}
	if metadata.until != nil && (terms.ValidUntilLevel == nil || metadata.until.Level < *terms.ValidUntilLevel) {
		terms.ValidUntil, terms.ValidUntilLevel = &metadata.until.Timestamp, &metadata.until.Level
	}
	return terms, true, nil
}

func inside(at time.Time, start, end *time.Time) bool {
	return (start == nil || !at.Before(*start)) && (end == nil || !at.After(*end))
}

func (h *History) readSnapshot(ctx context.Context) (*snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	s, loadErr, expires := h.snapshot, h.loadErr, h.expires
	h.mu.Unlock()
	// A transfer ahead of pinned coverage waits for the next refresh rather
	// than reloading the complete history separately for each row in a batch.
	if time.Now().Before(expires) {
		return s, loadErr
	}
	ch := h.flight.DoChan("history", func() (any, error) {
		// A caller can miss the fast path just before another fill completes.
		h.mu.Lock()
		s, loadErr, expires := h.snapshot, h.loadErr, h.expires
		h.mu.Unlock()
		if time.Now().Before(expires) {
			return s, loadErr
		}
		fillCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		s, err := h.load(fillCtx)
		ttl := historyCacheTTL
		if err != nil {
			// One outage must not trigger a full upstream retry per batch row.
			ttl = historyFailureTTL
		}
		h.mu.Lock()
		h.snapshot, h.loadErr, h.expires = s, err, time.Now().Add(ttl)
		h.mu.Unlock()
		return s, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return nil, result.Err
		}
		return result.Val.(*snapshot), nil
	}
}

func (h *History) load(ctx context.Context) (*snapshot, error) {
	var current head
	if err := h.get(ctx, "/head", &current); err != nil {
		return nil, err
	}
	if current.Chain != basenet || current.Level <= 2 {
		return nil, app.PrimaryMissing("network_mismatch")
	}
	// Keep the still-changing tip out of the proven interval.
	var pinned head
	if err := h.get(ctx, "/blocks/"+strconv.FormatInt(current.Level-2, 10), &pinned); err != nil {
		return nil, err
	}
	if pinned.Level != current.Level-2 || pinned.Hash == "" || pinned.Timestamp.IsZero() {
		return nil, app.PrimaryMissing("chain_coverage_missing")
	}
	s := &snapshot{head: pinned}
	launchpads, err := h.launchpads(ctx)
	if err != nil {
		return nil, err
	}
	rows := 0
	for _, launchpad := range launchpads {
		var storage struct {
			LaunchLedger int64 `json:"launchLedger"`
		}
		if err := h.get(ctx, "/contracts/"+launchpad+"/storage", &storage); err != nil {
			return nil, err
		}
		if storage.LaunchLedger <= 0 {
			return nil, app.PrimaryMissing("history_unavailable")
		}
		path := "/bigmaps/" + strconv.FormatInt(storage.LaunchLedger, 10)
		var keys []struct {
			Hash string `json:"hash"`
			Key  string `json:"key"`
		}
		if err := h.get(ctx, path+"/keys?limit="+strconv.Itoa(launchKeyLimit), &keys); err != nil {
			return nil, err
		}
		if len(keys) >= launchKeyLimit {
			return nil, app.PrimaryMissing("history_budget_exceeded")
		}
		for _, key := range keys {
			if key.Hash == "" {
				return nil, app.PrimaryMissing("history_unavailable")
			}
			history := launchHistory{launchpad: launchpad, key: key.Key}
			lastID := int64(0)
			for offset := 0; ; offset += historyPageSize {
				var page []update
				query := url.Values{"limit": {strconv.Itoa(historyPageSize)}, "offset": {strconv.Itoa(offset)}, "sort.asc": {"id"}, "level.le": {strconv.FormatInt(pinned.Level, 10)}}
				if err := h.get(ctx, path+"/keys/"+url.PathEscape(key.Hash)+"/updates?"+query.Encode(), &page); err != nil {
					return nil, err
				}
				rows += len(page)
				if rows > historyRowLimit {
					return nil, app.PrimaryMissing("history_budget_exceeded")
				}
				for _, u := range page {
					if u.ID <= lastID || u.Level <= 0 || u.Timestamp.IsZero() {
						return nil, app.PrimaryMissing("history_unavailable")
					}
					switch u.Action {
					case "remove_key":
						// Some indexers retain the previous value on removal.
						u.Value = nil
					case "add_key", "update_key":
						if u.Value == nil {
							return nil, app.PrimaryMissing("history_incomplete")
						}
					default:
						return nil, app.PrimaryMissing("history_incomplete")
					}
					lastID = u.ID
					if u.Level <= pinned.Level {
						history.updates = append(history.updates, u)
					}
				}
				if len(page) < historyPageSize {
					break
				}
			}
			sort.Slice(history.updates, func(i, j int) bool {
				if history.updates[i].Level != history.updates[j].Level {
					return history.updates[i].Level < history.updates[j].Level
				}
				return history.updates[i].ID < history.updates[j].ID
			})
			if len(history.updates) > 0 && history.updates[0].Action != "add_key" {
				return nil, app.PrimaryMissing("history_incomplete")
			}
			// The ledger also changes on every purchase. Keep only changes to
			// valuation inputs, so hundreds of wallet flows reuse a short price
			// timeline instead of repeatedly scanning purchase counters.
			versions := make([]update, 0)
			lastSignature := ""
			for _, u := range history.updates {
				sig := signature(u.Value)
				if len(versions) == 0 || sig != lastSignature {
					versions = append(versions, u)
					lastSignature = sig
				}
			}
			history.updates = versions
			s.histories = append(s.histories, history)
		}
	}
	payments, err := h.loadPayments(ctx, s.histories, pinned.Level, &rows)
	if err != nil {
		return nil, err
	}
	for i := range s.histories {
		s.histories[i].payments = payments
	}
	var checked head
	if err := h.get(ctx, "/blocks/"+strconv.FormatInt(pinned.Level, 10), &checked); err != nil {
		return nil, err
	}
	if checked.Hash != pinned.Hash {
		return nil, app.PrimaryMissing("chain_changed")
	}
	return s, nil
}

func (h *History) get(ctx context.Context, path string, out any) error {
	err := h.client.Get(ctx, path, out)
	if errors.Is(err, mvkt.ErrResponseTooLarge) {
		return app.PrimaryMissing("history_budget_exceeded")
	}
	return err
}

func validContract(address string) bool {
	return len(address) == 36 && strings.HasPrefix(address, "KT1") && !strings.ContainsAny(address, "/:? \t\n")
}

func (h *History) launchpads(ctx context.Context) ([]string, error) {
	var allowlist struct {
		SchemaVersion int      `json:"schema_version"`
		Launchpads    []string `json:"launchpads"`
	}
	if err := h.client.GetURL(ctx, h.allowlistURL, &allowlist); err != nil {
		return nil, app.PrimaryMissing("allowlist_unavailable")
	}
	if allowlist.SchemaVersion != 1 || allowlist.Launchpads == nil || len(allowlist.Launchpads) > 4 {
		return nil, app.PrimaryMissing("allowlist_unavailable")
	}
	seen := map[string]bool{}
	addresses := []string{}
	for _, address := range allowlist.Launchpads {
		if !validContract(address) {
			return nil, app.PrimaryMissing("allowlist_unavailable")
		}
		if !seen[address] {
			addresses = append(addresses, address)
			seen[address] = true
		}
	}
	return addresses, nil
}
