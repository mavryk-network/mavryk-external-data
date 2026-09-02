package equiteez

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/graphql"
	"quotes/internal/core/infrastructure/httpclient"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
)

const serviceName = "equiteez"

type Client struct {
	indexerURL string
	httpClient *http.Client
	logger     *zerolog.Logger
}

// NewClient builds an Equiteez GraphQL/Hasura HTTP client (rate limit + retry/CB
// from api, shared limiter key "equiteez").
func NewClient(eq config.EquiteezConfig, api *config.APIConfig, timeout time.Duration, logger *zerolog.Logger) *Client {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	componentLogger := logging.WithComponent(logger, "equiteez_client")
	res := api.OutboundResilience("equiteez")
	rl := eq.RateLimit.Settings("equiteez")
	var maxBytes int64
	if api != nil {
		maxBytes = api.OutboundMaxResponseBytes
	}
	rt := httpclient.MaxBytesReader(httpclient.SharedTransport(), maxBytes)
	rt = httpclient.WrapResilientTransport(rt, res)
	rt = httpclient.WrapCircuitBreaker(rt, res)
	rt = &logging.HTTPTransport{
		Base:      rt,
		Logger:    componentLogger,
		Component: "equiteez",
	}
	rt = httpclient.WrapRateLimited(rt, rl)
	return &Client{
		indexerURL: indexerRequestURL(eq.IndexerURL, eq.IndexerPassword),
		httpClient: &http.Client{
			Timeout:       timeout,
			Transport:     rt,
			CheckRedirect: httpclient.SameHostRedirectPolicy,
		},
		logger: componentLogger,
	}
}

// indexerRequestURL builds the URL for every indexer GraphQL request.
//
// Auth lives in the Cloudflare worker fronting the indexer: it injects the
// Hasura admin-secret and authorizes deployed callers by origin, so we send no
// secret header. Local/CI have no allowed origin, so a `?bypass=<secret>` param
// stands in — appended only when the password is set.
func indexerRequestURL(rawURL, password string) string {
	if password == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("bypass", password)
	u.RawQuery = q.Encode()
	return u.String()
}

// GetAllowlistedTokensAndOrderbooks returns every allowlisted token with its
// allowlisted orderbooks — the discovery source for `rwa_pairs`.
func (c *Client) GetAllowlistedTokensAndOrderbooks(ctx context.Context) ([]TokenWithOrderbooks, error) {
	query := `
		query allowlistedTokensWithOrderbooks {
			token(where: { in_allowlist: { _eq: true } }) {
				address
				token_id
				in_allowlist
				token_metadata
				token_standard
				metadata
				orderbooks(where: { in_allowlist: { _eq: true } }) {
					id
					address
					in_allowlist
					last_matched_price
					lowest_sell_price
					highest_buy_price
					sell_order_fee
					buy_order_fee
					currencies(limit: 1, where: { token: { address: { _is_null: false } } }) {
						currency_name
						token { address token_id }
					}
				}
			}
		}
	`
	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, nil, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Tokens []TokenWithOrderbooks `json:"token"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal allowlisted tokens: %w", err)
	}
	return result.Tokens, nil
}

// GetTokensWithOrderbooks queries tokens with orderbook data for multiple addresses.
func (c *Client) GetTokensWithOrderbooks(ctx context.Context, addresses []string) ([]TokenWithOrderbooks, error) {
	query := `
		query tokensWithOrderbooks($addresses: [String!]) {
			token(
				where: { 
					orderbooks: { id: { _is_null: false } },
					address: { _in: $addresses }
				}
			) {
				orderbooks {
					address
					last_matched_price
					lowest_sell_price
					highest_buy_price
					sell_order_fee
					buy_order_fee
					currencies(limit: 1, where: { token: { address: { _is_null: false } } }) {
						token {
							address
							token_id
						}
						currency_name
					}
				}
				token_id
				token_metadata
				token_standard
				metadata
				address
			}
		}
	`

	variables := map[string]interface{}{
		"addresses": addresses,
	}

	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, variables, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Tokens []TokenWithOrderbooks `json:"token"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tokens with orderbooks: %w", err)
	}

	return result.Tokens, nil
}

// GetFilledOrderbookOrders returns up to `limit` filled orders for the given
// orderbook in FILL order — `ended_at ASC, id ASC` — resuming strictly after
// `cursor`. Used by the Equiteez backfill job.
//
// Filters:
//   - orderbook_id     = $orderbook_id
//   - fulfilled_amount > 0             (skip never-matched orders)
//   - ended_at IS NOT NULL             (skip still-open orders; live collector covers them)
//   - keyset resume, or ended_at >= $start_from on a cold start (operator floor)
//
// Why (ended_at, id) and not id alone: `orderbook_order.id` is assigned when the
// order is CREATED, not when it fills. A resting limit order created early can
// fill long after the walk passed its id, and an id-only cursor
// (`id > $since_id`) would exclude it forever — its trade would never reach
// rwa_quote_prices, and the live collector cannot recover it because it
// snapshots bid/ask/last rather than replaying the event log. Walking by fill
// time cannot miss it: its ended_at is necessarily >= the cursor when it fills.
// `id` remains only as the tie-break for orders sharing an ended_at (same block),
// which keeps pagination stable across a batch boundary inside such a group.
//
// orderbookID and cursor.ID are int64 in Go (defensive — the indexer exposes
// both ids as the `Int` GraphQL scalar); JSON encoding handles the narrowing.
// startFrom may be the zero time.Time to disable the ended_at floor; it applies
// only on a cold start, since a live cursor already implies the floor.
func (c *Client) GetFilledOrderbookOrders(
	ctx context.Context,
	orderbookID int64,
	cursor OrderCursor,
	startFrom time.Time,
	limit int,
) ([]OrderbookOrder, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive: %d", limit)
	}
	const orderFields = `
					id
					order_type
					price_per_rwa_token
					fulfilled_amount
					ended_at
					operation_hash`

	var query string
	variables := map[string]interface{}{
		"orderbook_id": orderbookID,
		"limit":        limit,
	}
	switch {
	case cursor.Set():
		// Keyset resume: everything that filled after the cursor instant, plus
		// the remainder of the group that shares the cursor's ended_at.
		// RFC3339Nano keeps sub-second precision — truncating could re-fetch
		// (harmless, writes are idempotent) or skip (data loss).
		variables["since_ts"] = cursor.EndedAt.UTC().Format(time.RFC3339Nano)
		variables["since_id"] = cursor.ID
		query = fmt.Sprintf(`
			query filledOrderbookOrders($orderbook_id: Int!, $since_ts: timestamptz!, $since_id: Int!, $limit: Int!) {
				orderbook_order(
					where: {
						orderbook_id:     { _eq: $orderbook_id }
						fulfilled_amount: { _gt: "0" }
						ended_at:         { _is_null: false }
						_or: [
							{ ended_at: { _gt: $since_ts } }
							{ _and: [ { ended_at: { _eq: $since_ts } }, { id: { _gt: $since_id } } ] }
						]
					}
					order_by: [{ ended_at: asc }, { id: asc }]
					limit:    $limit
				) {%s
				}
			}
		`, orderFields)
	case !startFrom.IsZero():
		variables["start_from"] = startFrom.UTC().Format(time.RFC3339)
		query = fmt.Sprintf(`
			query filledOrderbookOrders($orderbook_id: Int!, $start_from: timestamptz!, $limit: Int!) {
				orderbook_order(
					where: {
						orderbook_id:     { _eq: $orderbook_id }
						fulfilled_amount: { _gt: "0" }
						ended_at:         { _gte: $start_from }
					}
					order_by: [{ ended_at: asc }, { id: asc }]
					limit:    $limit
				) {%s
				}
			}
		`, orderFields)
	default:
		query = fmt.Sprintf(`
			query filledOrderbookOrders($orderbook_id: Int!, $limit: Int!) {
				orderbook_order(
					where: {
						orderbook_id:     { _eq: $orderbook_id }
						fulfilled_amount: { _gt: "0" }
						ended_at:         { _is_null: false }
					}
					order_by: [{ ended_at: asc }, { id: asc }]
					limit:    $limit
				) {%s
				}
			}
		`, orderFields)
	}
	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, variables, nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Orders []OrderbookOrder `json:"orderbook_order"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal filled orderbook orders: %w", err)
	}
	return result.Orders, nil
}
