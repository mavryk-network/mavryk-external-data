package mvkt

import (
	"context"
	"math/big"
	"time"

	rwa "quotes/internal/core/domain/valuation"
)

type SupplySource interface {
	GetSupplyHead(context.Context) (SupplyHead, error)
	GetBlockIdentity(context.Context, int64) (rwa.SupplyBlock, error)
	ResolveSingleFA2Token(context.Context, string) (SupplyToken, error)
	GetTokenSupplyAt(context.Context, rwa.SupplyIdentity, int64) (string, error)
	GetTokenBalanceAt(context.Context, rwa.SupplyIdentity, string, int64) (string, error)
	GetLaunchpadTreasuriesAt(context.Context, []string, int64) (SupplyTreasuries, error)
}

// A context is immutable and shared by every asset in one bounded refresh.
type SupplyContext struct {
	Head          SupplyHead
	Block         rwa.SupplyBlock
	Treasuries    SupplyTreasuries
	TreasuryError error
}

type SupplyAmounts struct {
	TokenID          string
	Total            string
	Excluded         string
	Circulating      string
	CirculatingError error
}

func ValidateSupplyBlock(b rwa.SupplyBlock, now time.Time, lag time.Duration) error {
	if b.Level <= 0 || b.Hash == "" || b.Timestamp.IsZero() {
		return SupplyError("invalid_response")
	}
	if b.Timestamp.After(now.Add(30 * time.Second)) {
		return SupplyError("invalid_timestamp")
	}
	if now.Sub(b.Timestamp) > lag {
		return SupplyError("indexer_lag")
	}
	return nil
}

func PrepareSupplyContext(ctx context.Context, source SupplySource, chainID string, blockLag int64, launchpads []string, now time.Time, maxLag time.Duration) (*SupplyContext, error) {
	head, err := source.GetSupplyHead(ctx)
	if err != nil {
		return nil, err
	}
	scope := &SupplyContext{Head: head}
	if err := head.Validate(chainID, now, maxLag); err != nil {
		return scope, err
	}
	level := head.Level - blockLag
	if blockLag < 0 || level <= 0 {
		return scope, SupplyError("indexer_state_unknown")
	}
	block, err := source.GetBlockIdentity(ctx, level)
	if err != nil {
		return scope, err
	}
	if block.Level != level {
		return scope, SupplyError("invalid_response")
	}
	if err := ValidateSupplyBlock(block, now, maxLag); err != nil {
		return scope, err
	}
	treasuries, treasuryErr := source.GetLaunchpadTreasuriesAt(ctx, launchpads, level)
	if treasuries == nil && treasuryErr == nil {
		treasuryErr = SupplyError("invalid_response")
	}
	return &SupplyContext{Head: head, Block: block, Treasuries: treasuries, TreasuryError: treasuryErr}, nil
}

// HistoricalSupplyContext verifies chain/coverage and branch without applying
// the live head's age budget to a historical block.
func HistoricalSupplyContext(ctx context.Context, source SupplySource, chainID string, level int64, launchpads []string) (*SupplyContext, error) {
	h, err := source.GetSupplyHead(ctx)
	if err != nil {
		return nil, err
	}
	if h.ChainID != chainID {
		return nil, SupplyError("network_mismatch")
	}
	if level <= 0 || level > h.Level {
		return nil, SupplyError("indexer_state_unknown")
	}
	b, err := source.GetBlockIdentity(ctx, level)
	if err != nil {
		return nil, err
	}
	t, err := source.GetLaunchpadTreasuriesAt(ctx, launchpads, level)
	return &SupplyContext{Head: h, Block: b, Treasuries: t, TreasuryError: err}, nil
}

// All amounts and treasury membership refer to the same block. The current
// /tokens endpoint resolves identity only; its current total is never used.
func ReadSupplyAmounts(ctx context.Context, source SupplySource, contract string, scope *SupplyContext, now func() time.Time, maxLag time.Duration) (SupplyAmounts, error) {
	var result SupplyAmounts
	identity, err := source.ResolveSingleFA2Token(ctx, contract)
	if err != nil {
		return result, err
	}
	if identity.Contract != contract || !rwa.ValidSupplyNat(identity.TokenID, 256) {
		return result, SupplyError("invalid_response")
	}
	if identity.FirstLevel > scope.Block.Level {
		return result, SupplyError("not_observed")
	}
	result.TokenID = identity.TokenID
	id := rwa.SupplyIdentity{Contract: contract, TokenID: identity.TokenID}
	result.Total, err = source.GetTokenSupplyAt(ctx, id, scope.Block.Level)
	if err != nil {
		return result, err
	}
	if !rwa.ValidSupplyNat(result.Total, 1024) {
		return result, SupplyError("invalid_response")
	}
	result.CirculatingError = scope.TreasuryError
	excluded := new(big.Int)
	if result.CirculatingError == nil {
		seen := map[string]bool{}
		for _, address := range scope.Treasuries[SupplyTokenKey{contract, identity.TokenID}] {
			if seen[address] {
				continue
			}
			seen[address] = true
			balance, err := source.GetTokenBalanceAt(ctx, id, address, scope.Block.Level)
			if err != nil {
				result.CirculatingError = err
				break
			}
			if !rwa.ValidSupplyNat(balance, 1024) {
				result.CirculatingError = SupplyError("invalid_response")
				break
			}
			n, _ := new(big.Int).SetString(balance, 10)
			excluded.Add(excluded, n)
		}
	}
	// Check even after a balance error: branch evidence must invalidate old data,
	// rather than silently retaining a snapshot from an orphaned block.
	after, err := source.GetBlockIdentity(ctx, scope.Block.Level)
	if err != nil {
		return result, err
	}
	if after.Level != scope.Block.Level || after.Hash != scope.Block.Hash {
		return result, SupplyError("chain_reorg")
	}
	if err := ValidateSupplyBlock(after, now(), maxLag); err != nil {
		return result, err
	}
	if result.CirculatingError == nil {
		total, _ := new(big.Int).SetString(result.Total, 10)
		if excluded.Cmp(total) > 0 {
			result.CirculatingError = SupplyError("inconsistent_snapshot")
		} else {
			result.Excluded = excluded.String()
			result.Circulating = new(big.Int).Sub(total, excluded).String()
		}
	}
	return result, nil
}
