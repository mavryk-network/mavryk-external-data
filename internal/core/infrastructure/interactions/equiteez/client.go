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
	rt = &logging.HTTPTransport{
		Base:      rt,
		Logger:    componentLogger,
		Component: "equiteez",
	}
	rt = httpclient.WrapRateLimited(rt, rl)
	return &Client{
		indexerURL: indexerRequestURL(eq.IndexerURL, eq.IndexerPassword),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: rt,
		},
		logger: componentLogger,
	}
}

// indexerRequestURL builds the URL used for every indexer GraphQL request.
//
// Auth lives in the equiteez Cloudflare worker (e.g. basenet.api.equiteez.com): it
// injects the Hasura admin-secret itself and authorizes deployed callers by
// origin/domain, so the backend sends NO x-hasura-admin-secret header.
//
// Local dev and CI tests have no allowed origin, so the worker also accepts a
// `?bypass=<secret>` query param in place of the header. When the password is set
// (local/CI only) we append it once here; in deployed (in-cluster) envs it is empty
// and the URL is used as-is.
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

// GetAllowlistedTokensAndOrderbooks discovers active RWA pairs from the indexer:
// returns every token where `in_allowlist=true` together with its orderbooks
// where `in_allowlist=true`. The sync job (jobs/equiteez_rwa_sync.go) calls
// this at startup and upserts the result into `rwa_pairs`.
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
// orderbook, ordered by id ascending and starting strictly after `sinceID`.
// Used by the Equiteez backfill job.
//
// Filters:
//   - orderbook_id  = $orderbook_id
//   - id            > $since_id        (forward cursor; 0 = start of history)
//   - fulfilled_amount > 0              (skip never-matched orders)
//   - ended_at IS NOT NULL              (skip still-open orders; live collector covers them)
//   - ended_at      >= $start_from     (only when startFrom is non-zero — operator-imposed floor)
//
// We forward-walk by `id ASC` because:
//   - id is monotonic.
//   - ended_at can have ties at second-level resolution within a single block.
//   - Using id avoids pagination tie-breaker logic.
//
// orderbookID and sinceID are passed as int64 in Go (defensive — int32 would
// suffice today since the indexer exposes both `orderbook.id` and
// `orderbook_order.id` as the `Int` GraphQL scalar). Values fit comfortably;
// JSON encoding handles the narrowing on the wire. startFrom may be the zero
// time.Time to disable the ended_at floor.
func (c *Client) GetFilledOrderbookOrders(
	ctx context.Context,
	orderbookID int64,
	sinceID int64,
	startFrom time.Time,
	limit int,
) ([]OrderbookOrder, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit must be positive: %d", limit)
	}
	var query string
	variables := map[string]interface{}{
		"orderbook_id": orderbookID,
		"since_id":     sinceID,
		"limit":        limit,
	}
	if startFrom.IsZero() {
		query = `
			query filledOrderbookOrders($orderbook_id: Int!, $since_id: Int!, $limit: Int!) {
				orderbook_order(
					where: {
						orderbook_id:     { _eq: $orderbook_id }
						id:               { _gt: $since_id }
						fulfilled_amount: { _gt: "0" }
						ended_at:         { _is_null: false }
					}
					order_by: { id: asc }
					limit:    $limit
				) {
					id
					order_type
					price_per_rwa_token
					fulfilled_amount
					ended_at
					operation_hash
				}
			}
		`
	} else {
		query = `
			query filledOrderbookOrders($orderbook_id: Int!, $since_id: Int!, $start_from: timestamptz!, $limit: Int!) {
				orderbook_order(
					where: {
						orderbook_id:     { _eq: $orderbook_id }
						id:               { _gt: $since_id }
						fulfilled_amount: { _gt: "0" }
						ended_at:         { _gte: $start_from }
					}
					order_by: { id: asc }
					limit:    $limit
				) {
					id
					order_type
					price_per_rwa_token
					fulfilled_amount
					ended_at
					operation_hash
				}
			}
		`
		variables["start_from"] = startFrom.UTC().Format(time.RFC3339)
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
