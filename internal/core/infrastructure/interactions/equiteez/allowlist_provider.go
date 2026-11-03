package equiteez

import (
	"context"
	"fmt"
	"time"
)

// AllowlistSnapshot preserves the CRM read time and durable scope version.
// Historical launchpads remain part of treasury proof after CRM delists them.
type AllowlistSnapshot struct {
	Document           ContractAllowlist `json:"document"`
	Version            string            `json:"version"`
	FetchedAt          time.Time         `json:"fetched_at"`
	ValidFrom          time.Time         `json:"valid_from"`
	SourceStatus       string            `json:"source_status"`
	TreasuryLaunchpads []string          `json:"treasury_launchpads"`
}

type AllowlistReader interface {
	Snapshot(context.Context) (AllowlistSnapshot, error)
}
type AllowlistStore interface {
	AllowlistSnapshot(context.Context, string, *time.Time) (AllowlistSnapshot, error)
	ApplyAllowlist(context.Context, string, ContractAllowlist, bool) error
	WithAllowlistLock(context.Context, func() error) error
	RecordAllowlistFailure(context.Context, string) error
}

type AllowlistProvider struct {
	Client *Client
	Store  AllowlistStore
	URL    string
}

func (p *AllowlistProvider) Snapshot(ctx context.Context) (AllowlistSnapshot, error) {
	return p.Store.AllowlistSnapshot(ctx, p.URL, nil)
}

func (p *AllowlistProvider) At(ctx context.Context, at time.Time) (AllowlistSnapshot, error) {
	return p.Store.AllowlistSnapshot(ctx, p.URL, &at)
}

// Refresh owns the only CRM fetch path. A failed fetch retains the same saved
// version and fetched_at; it never rewrites a fallback as a fresh observation.
func (p *AllowlistProvider) Refresh(ctx context.Context) (AllowlistSnapshot, error) {
	var out AllowlistSnapshot
	err := p.Store.WithAllowlistLock(ctx, func() error {
		cached, cachedErr := p.Snapshot(ctx)
		// Preserve the catalog's existing budget: CRM may spend only a third
		// of the remaining tick, leaving time for persisted fallback/indexer.
		budget := 10 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			budget = time.Until(deadline) / 3
		}
		fetchCtx, cancel := context.WithTimeout(ctx, budget)
		doc, err := p.Client.GetContractAllowlist(fetchCtx, p.URL)
		cancel()
		if err != nil {
			if cachedErr != nil {
				return fmt.Errorf("allowlist_unavailable: %w", err)
			}
			out = cached
			if e := p.Store.ApplyAllowlist(ctx, p.URL, cached.Document, false); e != nil {
				return e
			}
			if e := p.Store.RecordAllowlistFailure(ctx, p.URL); e != nil {
				return e
			}
			out.SourceStatus = "crm_unavailable"
			return nil
		}
		if err := p.Store.ApplyAllowlist(ctx, p.URL, doc, true); err != nil {
			return err
		}
		out, err = p.Snapshot(ctx)
		return err
	})
	return out, err
}
