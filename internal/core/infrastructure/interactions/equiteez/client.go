package equiteez

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"quotes/internal/config"
	"quotes/internal/core/infrastructure/graphql"
	"quotes/internal/core/infrastructure/httpclient"
	"quotes/internal/logging"

	"github.com/rs/zerolog"
)

const serviceName = "equiteez"

type Client struct {
	indexerURL           string
	tokenIndexerURL      string
	indexerPassword      string
	tokenIndexerPassword string
	httpClient           *http.Client
	logger               *zerolog.Logger
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
		indexerURL:           eq.IndexerURL,
		tokenIndexerURL:      eq.TokenIndexerURL,
		indexerPassword:      eq.IndexerPassword,
		tokenIndexerPassword: eq.TokenIndexerPassword,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: rt,
		},
		logger: componentLogger,
	}
}

func (c *Client) headersForURL(url string) map[string]string {
	var password string
	switch url {
	case c.indexerURL:
		password = c.indexerPassword
	case c.tokenIndexerURL:
		password = c.tokenIndexerPassword
	}
	if password != "" {
		return map[string]string{"x-hasura-admin-secret": password}
	}
	return nil
}

// GetRWATransfers queries RWA transfer history from the Equiteez indexer (GraphQL).
func (c *Client) GetRWATransfers(ctx context.Context, walletAddress, assetAddress string, limit, offset int) ([]RWATransfer, error) {
	query := `
		query GetRWATransfers($wallet: String!, $asset: String!, $limit: Int!, $offset: Int!) {
			rwaTransfers(wallet: $wallet, asset: $asset, limit: $limit, offset: $offset) {
				id
				hash
				type
				level
				timestamp
				sender
				target
				amount
				tokenId
				contract
			}
		}
	`

	variables := map[string]interface{}{
		"wallet": walletAddress,
		"asset":  assetAddress,
		"limit":  limit,
		"offset": offset,
	}

	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, variables, c.headersForURL(c.indexerURL))
	if err != nil {
		return nil, err
	}

	var result struct {
		RWATransfers []RWATransfer `json:"rwaTransfers"`
	}

	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal RWA transfers: %w", err)
	}

	return result.RWATransfers, nil
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
	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, nil, c.headersForURL(c.indexerURL))
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

	data, err := graphql.Execute(ctx, c.httpClient, serviceName, c.indexerURL, query, variables, c.headersForURL(c.indexerURL))
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
