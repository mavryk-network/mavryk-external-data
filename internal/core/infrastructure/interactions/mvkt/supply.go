package mvkt

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"time"

	rwa "quotes/internal/core/domain/valuation"
)

type SupplyError string

func (e SupplyError) Error() string { return string(e) }
func SupplyReason(err error) string {
	var e SupplyError
	if errors.As(err, &e) {
		return string(e)
	}
	return "http_error"
}

type SupplyHead struct {
	rwa.SupplyBlock
	ChainID    string    `json:"chainId"`
	KnownLevel int64     `json:"knownLevel"`
	LastSync   time.Time `json:"lastSync"`
	Synced     bool      `json:"synced"`
}

func (h SupplyHead) Validate(chain string, now time.Time, lag time.Duration) error {
	if h.ChainID != chain {
		return SupplyError("network_mismatch")
	}
	if h.Level <= 0 || h.Hash == "" || h.Timestamp.IsZero() || h.LastSync.IsZero() {
		return SupplyError("indexer_state_unknown")
	}
	if h.Timestamp.After(now.Add(30*time.Second)) || h.LastSync.After(now.Add(30*time.Second)) {
		return SupplyError("invalid_timestamp")
	}
	if !h.Synced || h.KnownLevel < h.Level || h.KnownLevel-h.Level > 2 || now.Sub(h.Timestamp) > lag || now.Sub(h.LastSync) > lag {
		return SupplyError("indexer_lag")
	}
	return nil
}

type SupplyToken struct {
	Contract, TokenID string
	FirstLevel        int64
}
type SupplyLimits struct{ PageSize, MaxRows, MaxResponseBytes int }

func (l SupplyLimits) defaults() SupplyLimits {
	if l.PageSize <= 0 {
		l.PageSize = 100
	}
	if l.MaxRows <= 0 {
		l.MaxRows = 1000
	}
	if l.MaxResponseBytes <= 0 {
		l.MaxResponseBytes = 16 << 20
	}
	return l
}
func (c *Client) WithSupplyLimits(l SupplyLimits) *Client { c.supplyLimits = l.defaults(); return c }
func (c *Client) supplyJSON(ctx context.Context, path string, dst interface{}) error {
	b, err := c.getJSONLimited(ctx, c.baseURL+path, int64(c.supplyLimits.defaults().MaxResponseBytes))
	if err != nil {
		return err
	}
	if json.Unmarshal(b, dst) != nil {
		return SupplyError("invalid_response")
	}
	return nil
}
func (c *Client) GetSupplyHead(ctx context.Context) (SupplyHead, error) {
	var h SupplyHead
	err := c.supplyJSON(ctx, "/head", &h)
	return h, err
}
func (c *Client) GetBlockIdentity(ctx context.Context, level int64) (rwa.SupplyBlock, error) {
	var b rwa.SupplyBlock
	err := c.supplyJSON(ctx, "/blocks/"+strconv.FormatInt(level, 10), &b)
	if err != nil {
		return b, err
	}
	if b.Level != level || b.Hash == "" || b.Timestamp.IsZero() {
		return b, SupplyError("invalid_response")
	}
	return b, nil
}

type supplyTokenJSON struct {
	Contract struct {
		Address string `json:"address"`
	} `json:"contract"`
	TokenID    json.RawMessage `json:"tokenId"`
	Standard   string          `json:"standard"`
	FirstLevel int64           `json:"firstLevel"`
}

func (c *Client) ResolveSingleFA2Token(ctx context.Context, contract string) (SupplyToken, error) {
	v := url.Values{"contract": {contract}, "limit": {"2"}}
	var rows []supplyTokenJSON
	if err := c.supplyJSON(ctx, "/tokens?"+v.Encode(), &rows); err != nil {
		return SupplyToken{}, err
	}
	if rows == nil {
		return SupplyToken{}, SupplyError("invalid_response")
	}
	if len(rows) == 0 {
		return SupplyToken{}, SupplyError("token_not_found")
	}
	if len(rows) != 1 {
		return SupplyToken{}, SupplyError("identity_ambiguous")
	}
	r := rows[0]
	tid, err := rwa.ParseSupplyNat(r.TokenID, 256)
	if err != nil || r.Contract.Address != contract || r.Standard != "fa2" {
		return SupplyToken{}, SupplyError("invalid_response")
	}
	if r.FirstLevel <= 0 {
		return SupplyToken{}, SupplyError("invalid_response")
	}
	return SupplyToken{Contract: contract, TokenID: tid, FirstLevel: r.FirstLevel}, nil
}

type SupplyTokenKey struct{ Contract, TokenID string }
type SupplyTreasuries map[SupplyTokenKey][]string

type supplyStorage struct {
	TotalSupply    json.RawMessage `json:"total_supply"`
	TokenMetadata  json.RawMessage `json:"token_metadata"`
	LaunchLedger   json.RawMessage `json:"launchLedger"`
	TreasuryLedger json.RawMessage `json:"treasuryLedger"`
	IsKilled       bool            `json:"isKilled"`
}

// SupplyMetadataAt uses the same block as the balances and the historical
// token_metadata pointer, never today's metadata for an earlier observation.
func (c *Client) SupplyMetadataAt(ctx context.Context, id rwa.SupplyIdentity, block rwa.SupplyBlock) (rwa.Metadata, string, error) {
	out := rwa.Metadata{Source: "mvkt_inline", Block: &block}
	storage, err := c.supplyStorageAt(ctx, id.Contract, block.Level)
	if err != nil {
		return out, "", err
	}
	rows, err := c.supplyKeysAt(ctx, storage.TokenMetadata, block.Level, url.Values{"key": {id.TokenID}, "active": {"true"}, "limit": {"2"}})
	if err != nil {
		return out, "", err
	}
	if len(rows) != 1 {
		return out, "", SupplyError("metadata_unverified")
	}
	key, keyErr := rwa.ParseSupplyNat(rows[0].Key, 256)
	if keyErr != nil || key != id.TokenID {
		return out, "", SupplyError("metadata_unverified")
	}
	var value struct {
		TokenID json.RawMessage   `json:"token_id"`
		Info    map[string]string `json:"token_info"`
	}
	if json.Unmarshal(rows[0].Value, &value) != nil {
		return out, "", SupplyError("metadata_unverified")
	}
	tid, err := rwa.ParseSupplyNat(value.TokenID, 256)
	if err != nil || tid != id.TokenID {
		return out, "", SupplyError("metadata_unverified")
	}
	raw, err := hex.DecodeString(value.Info["decimals"])
	if err != nil {
		return out, "", SupplyError("decimals_unknown")
	}
	scale, ok := rwa.MetadataDecimals(map[string]interface{}{"decimals": string(raw)})
	if !ok {
		return out, "", SupplyError("decimals_unknown")
	}
	d := int(scale)
	out.Decimals = &d
	out.Version = rwa.Fingerprint(value)
	symbol, _ := hex.DecodeString(value.Info["symbol"])
	return out, string(symbol), nil
}

type supplyBigMapKey struct {
	ID     int64           `json:"id"`
	Active *bool           `json:"active"`
	Key    json.RawMessage `json:"key"`
	Value  json.RawMessage `json:"value"`
}

func (c *Client) supplyStorageAt(ctx context.Context, contract string, level int64) (supplyStorage, error) {
	var storage supplyStorage
	if contract == "" || level <= 0 {
		return storage, SupplyError("invalid_response")
	}
	err := c.supplyJSON(ctx, "/contracts/"+url.PathEscape(contract)+"/storage?level="+strconv.FormatInt(level, 10), &storage)
	return storage, err
}

func (c *Client) supplyKeysAt(ctx context.Context, pointer json.RawMessage, level int64, query url.Values) ([]supplyBigMapKey, error) {
	id, err := rwa.ParseSupplyNat(pointer, 19)
	if err != nil {
		return nil, SupplyError("source_contract_unsupported")
	}
	var rows []supplyBigMapKey
	err = c.supplyJSON(ctx, "/bigmaps/"+id+"/historical_keys/"+strconv.FormatInt(level, 10)+"?"+query.Encode(), &rows)
	if err != nil {
		return nil, err
	}
	limit, _ := strconv.Atoi(query.Get("limit"))
	if rows == nil || len(rows) > limit {
		return nil, SupplyError("invalid_response")
	}
	for _, row := range rows {
		if row.ID <= 0 || row.Active == nil || !*row.Active || len(row.Key) == 0 || len(row.Value) == 0 {
			return nil, SupplyError("invalid_response")
		}
	}
	return rows, nil
}

// Read storage first: big-map pointers may change after kill/reallocation.
// Missing data is not a zero supply; isKilled is the explicit RWA zero state.
func (c *Client) GetTokenSupplyAt(ctx context.Context, id rwa.SupplyIdentity, level int64) (string, error) {
	if !rwa.ValidSupplyNat(id.TokenID, 256) {
		return "", SupplyError("invalid_response")
	}
	storage, err := c.supplyStorageAt(ctx, id.Contract, level)
	if err != nil {
		return "", err
	}
	if _, err = rwa.ParseSupplyNat(storage.TotalSupply, 19); err != nil {
		return "", SupplyError("source_contract_unsupported")
	}
	if storage.IsKilled {
		return "0", nil
	}
	rows, err := c.supplyKeysAt(ctx, storage.TotalSupply, level, url.Values{"key": {id.TokenID}, "active": {"true"}, "limit": {"2"}})
	if err != nil {
		return "", err
	}
	if len(rows) != 1 {
		return "", SupplyError("invalid_response")
	}
	key, err := rwa.ParseSupplyNat(rows[0].Key, 256)
	if err != nil || key != id.TokenID {
		return "", SupplyError("invalid_response")
	}
	total, err := rwa.ParseSupplyNat(rows[0].Value, 1024)
	if err != nil {
		return "", SupplyError("invalid_response")
	}
	return total, nil
}

// A successful empty response for an exact token/account filter means zero.
// HTTP errors, null, mismatched identities and duplicate rows never mean zero.
func (c *Client) GetTokenBalanceAt(ctx context.Context, id rwa.SupplyIdentity, account string, level int64) (string, error) {
	if account == "" || id.Contract == "" || level <= 0 || !rwa.ValidSupplyNat(id.TokenID, 256) {
		return "", SupplyError("invalid_response")
	}
	q := url.Values{"token.contract": {id.Contract}, "token.tokenId": {id.TokenID}, "account": {account}, "limit": {"2"}}
	var rows []struct {
		Account struct {
			Address string `json:"address"`
		} `json:"account"`
		Token   supplyTokenJSON `json:"token"`
		Balance json.RawMessage `json:"balance"`
	}
	err := c.supplyJSON(ctx, "/tokens/historical_balances/"+strconv.FormatInt(level, 10)+"?"+q.Encode(), &rows)
	if err != nil {
		return "", err
	}
	if rows == nil || len(rows) > 1 {
		return "", SupplyError("invalid_response")
	}
	if len(rows) == 0 {
		return "0", nil
	}
	r := rows[0]
	tid, err := rwa.ParseSupplyNat(r.Token.TokenID, 256)
	if err != nil || tid != id.TokenID || r.Token.Contract.Address != id.Contract || r.Account.Address != account || r.Token.Standard != "fa2" {
		return "", SupplyError("invalid_response")
	}
	balance, err := rwa.ParseSupplyNat(r.Balance, 1024)
	if err != nil {
		return "", SupplyError("invalid_response")
	}
	return balance, nil
}

// launchpads is the complete configured scope. nil means unknown; an explicitly
// empty slice means this network has no launchpads. Never infer absence on error.
func (c *Client) GetLaunchpadTreasuriesAt(ctx context.Context, launchpads []string, level int64) (SupplyTreasuries, error) {
	if launchpads == nil {
		return nil, SupplyError("treasury_unavailable")
	}
	if level <= 0 {
		return nil, SupplyError("invalid_response")
	}
	result := SupplyTreasuries{}
	seenLaunchpads := map[string]bool{}
	seenRows := map[int64]bool{}
	limits := c.supplyLimits.defaults()
	count := 0
	for _, address := range launchpads {
		if seenLaunchpads[address] {
			continue
		}
		seenLaunchpads[address] = true
		storage, err := c.supplyStorageAt(ctx, address, level)
		if err != nil {
			return nil, err
		}
		tokens := map[SupplyTokenKey]bool{}
		for offset := 0; ; {
			limit := min(limits.PageSize, limits.MaxRows-count+1)
			rows, err := c.supplyKeysAt(ctx, storage.LaunchLedger, level, url.Values{"active": {"true"}, "sort.asc": {"id"}, "offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}})
			if err != nil {
				return nil, err
			}
			count += len(rows)
			if count > limits.MaxRows {
				return nil, SupplyError("pagination_incomplete")
			}
			for _, row := range rows {
				if seenRows[row.ID] {
					return nil, SupplyError("pagination_incomplete")
				}
				seenRows[row.ID] = true
				var launch struct {
					Contract string          `json:"tokenContractAddress"`
					TokenID  json.RawMessage `json:"tokenId"`
				}
				if json.Unmarshal(row.Value, &launch) != nil || launch.Contract == "" {
					return nil, SupplyError("invalid_response")
				}
				tid, err := rwa.ParseSupplyNat(launch.TokenID, 256)
				if err != nil {
					return nil, SupplyError("invalid_response")
				}
				tokens[SupplyTokenKey{launch.Contract, tid}] = true
			}
			if len(rows) < limit {
				break
			}
			offset += len(rows)
		}
		if len(tokens) == 0 {
			continue
		}
		rows, err := c.supplyKeysAt(ctx, storage.TreasuryLedger, level, url.Values{"key": {"launchTreasury"}, "active": {"true"}, "limit": {"2"}})
		if err != nil {
			return nil, err
		}
		if len(rows) != 1 {
			return nil, SupplyError("treasury_unavailable")
		}
		var key, treasury string
		if json.Unmarshal(rows[0].Key, &key) != nil || key != "launchTreasury" || json.Unmarshal(rows[0].Value, &treasury) != nil || treasury == "" {
			return nil, SupplyError("invalid_response")
		}
		for token := range tokens {
			duplicate := false
			for _, existing := range result[token] {
				duplicate = duplicate || existing == treasury
			}
			if !duplicate {
				result[token] = append(result[token], treasury)
			}
		}
	}
	for _, addresses := range result {
		sort.Strings(addresses)
	}
	return result, nil
}
