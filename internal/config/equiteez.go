package config

// EquiteezConfig holds Equiteez indexer (GraphQL/Hasura) client settings.
//
// RateLimit is independent from the CoinGecko one — the two services share no
// quota. Hasura admin-secret endpoints usually have no per-IP limit, so this
// defaults to 0 (disabled). Set equiteez.rate_limit.rps > 0 only if you know
// your indexer enforces a quota.
type EquiteezConfig struct {
	IndexerURL           string          `yaml:"indexer_url"`
	IndexerPassword      string          `yaml:"indexer_password"`
	TokenIndexerURL      string          `yaml:"token_indexer_url"`
	TokenIndexerPassword string          `yaml:"token_indexer_password"`
	RateLimit            RateLimitConfig `yaml:"rate_limit"`
}
