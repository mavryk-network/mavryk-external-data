package prices

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"

	"github.com/shopspring/decimal"
)

// fakeChangeRepo is a ChangeRepository test double. Captures call count
// and seen queries; returns a canned result or err.
type fakeChangeRepo struct {
	mu       sync.Mutex
	calls    int32
	canned   ChangeRepoResult
	err      error
	delay    time.Duration
	seenLast ChangeQuery
}

func (f *fakeChangeRepo) GetChange(_ context.Context, q ChangeQuery) (ChangeRepoResult, error) {
	atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	f.seenLast = q
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return ChangeRepoResult{}, f.err
	}
	return f.canned, nil
}

func (f *fakeChangeRepo) Calls() int { return int(atomic.LoadInt32(&f.calls)) }

func newServiceWithCanned(canned ChangeRepoResult) (*ChangeService, *fakeChangeRepo) {
	repo := &fakeChangeRepo{canned: canned}
	svc := &ChangeService{Repo: repo, Cache: NewChangeCache(), Kind: "fa"}
	return svc, repo
}

func TestChangeService_HappyPath_FT_OneCurrencyOnePeriod(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now: []ChangeNow{
			{Currency: "usd", Price: decimal.RequireFromString("0.071541"), TS: now, Found: true},
		},
		Anchors: []ChangeAnchor{
			{Currency: "usd", Period: prices.Period24h, Price: decimal.RequireFromString("0.072100"), Bucket: now.Add(-24 * time.Hour), Found: true},
		},
	}
	svc, _ := newServiceWithCanned(canned)
	res, err := svc.GetChange(context.Background(), ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.AsOf.Equal(now) {
		t.Errorf("AsOf = %v, want %v", res.AsOf, now)
	}
	cur := res.Currencies["usd"]
	if !cur.NowFound {
		t.Fatal("now must be found")
	}
	per := cur.ByPeriod[prices.Period24h]
	if !per.AnchorFound {
		t.Fatal("24h anchor must be found")
	}
	if !per.ChangePctValid {
		t.Fatal("change_pct must be valid")
	}
	// (0.071541 - 0.072100) / 0.072100 * 100 at 24 dp. A literal on purpose:
	// recomputing via the production formula would make this tautological.
	wantPct := decimal.RequireFromString("-0.775312066574202496532594")
	if !per.ChangePct.Equal(wantPct) {
		t.Errorf("change_pct = %s, want %s", per.ChangePct, wantPct)
	}
}

// A sub-micro move must produce a real change_pct, not a value the 16-dp
// default division would flatten. Golden literal, same rationale as above.
func TestChangeService_SubMicroChangePct(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now: []ChangeNow{
			{Currency: "btc", Price: decimal.RequireFromString("0.00000068464"), TS: now, Found: true},
		},
		Anchors: []ChangeAnchor{
			{Currency: "btc", Period: prices.Period24h, Price: decimal.RequireFromString("0.00000071541"), Bucket: now.Add(-24 * time.Hour), Found: true},
		},
	}
	svc, _ := newServiceWithCanned(canned)
	res, err := svc.GetChange(context.Background(), ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"btc"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	per := res.Currencies["btc"].ByPeriod[prices.Period24h]
	if !per.ChangePctValid {
		t.Fatal("change_pct must be valid")
	}
	wantPct := decimal.RequireFromString("-4.301030178499042507093834")
	if !per.ChangePct.Equal(wantPct) {
		t.Errorf("change_pct = %s, want %s", per.ChangePct, wantPct)
	}
	wantDelta := decimal.RequireFromString("-0.00000003077")
	if !per.DeltaAbs.Equal(wantDelta) {
		t.Errorf("delta_abs = %s, want %s", per.DeltaAbs, wantDelta)
	}
}

func TestChangeService_DivisionByZero_LeavesValidFalseButKeepsAnchor(t *testing.T) {
	// Decision #10: when p_then == 0, change_pct + delta_abs are null on
	// the wire, but from/from_ts still come through.
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now: []ChangeNow{{Currency: "usd", Price: decimal.RequireFromString("100"), TS: now, Found: true}},
		Anchors: []ChangeAnchor{
			{Currency: "usd", Period: prices.Period24h, Price: decimal.NewFromInt(0), Bucket: now.Add(-24 * time.Hour), Found: true},
		},
	}
	svc, _ := newServiceWithCanned(canned)
	res, err := svc.GetChange(context.Background(), ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	per := res.Currencies["usd"].ByPeriod[prices.Period24h]
	if !per.AnchorFound {
		t.Fatal("anchor must be found even with p_then==0")
	}
	if !per.FromPrice.IsZero() {
		t.Errorf("FromPrice = %s, want 0", per.FromPrice)
	}
	if per.ChangePctValid {
		t.Error("ChangePctValid must be false when p_then==0")
	}
}

func TestChangeService_MissingHistory_AnchorFoundFalse(t *testing.T) {
	// Token younger than 30d → repo returns Found=false for that period.
	// Service emits AnchorFound=false; HTTP layer renders as null.
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now: []ChangeNow{{Currency: "usd", Price: decimal.RequireFromString("100"), TS: now, Found: true}},
		Anchors: []ChangeAnchor{
			{Currency: "usd", Period: prices.Period24h, Price: decimal.RequireFromString("90"), Bucket: now.Add(-24 * time.Hour), Found: true},
			{Currency: "usd", Period: prices.Period30d, Found: false},
		},
	}
	svc, _ := newServiceWithCanned(canned)
	res, err := svc.GetChange(context.Background(), ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h, prices.Period30d},
		Now:        now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Currencies["usd"].ByPeriod[prices.Period24h].AnchorFound {
		t.Error("24h must be found")
	}
	if res.Currencies["usd"].ByPeriod[prices.Period30d].AnchorFound {
		t.Error("30d must NOT be found")
	}
}

func TestChangeService_CacheHit_SkipsRepo(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now:     []ChangeNow{{Currency: "usd", Price: decimal.RequireFromString("100"), TS: now, Found: true}},
		Anchors: []ChangeAnchor{{Currency: "usd", Period: prices.Period24h, Price: decimal.RequireFromString("90"), Bucket: now.Add(-24 * time.Hour), Found: true}},
	}
	svc, repo := newServiceWithCanned(canned)
	q := ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	}
	if _, err := svc.GetChange(context.Background(), q); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := svc.GetChange(context.Background(), q); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if repo.Calls() != 1 {
		t.Errorf("expected 1 repo call (second served from cache), got %d", repo.Calls())
	}
}

func TestChangeService_PreflightErrors(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		q       ChangeQuery
		wantMsg string
	}{
		{
			name:    "no source",
			q:       ChangeQuery{EntityKey: "mvrk", Currencies: []string{"usd"}, Periods: []prices.Period{prices.Period24h}, Now: now},
			wantMsg: "source",
		},
		{
			name:    "no entity",
			q:       ChangeQuery{Source: prices.SourceCoinGecko, Currencies: []string{"usd"}, Periods: []prices.Period{prices.Period24h}, Now: now},
			wantMsg: "entity",
		},
		{
			name:    "no currency",
			q:       ChangeQuery{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Periods: []prices.Period{prices.Period24h}, Now: now},
			wantMsg: "currency",
		},
		{
			name:    "no period",
			q:       ChangeQuery{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Currencies: []string{"usd"}, Now: now},
			wantMsg: "period",
		},
		{
			name:    "unsupported period",
			q:       ChangeQuery{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Currencies: []string{"usd"}, Periods: []prices.Period{"12h"}, Now: now},
			wantMsg: "unsupported period",
		},
		{
			name:    "no now",
			q:       ChangeQuery{Source: prices.SourceCoinGecko, EntityKey: "mvrk", Currencies: []string{"usd"}, Periods: []prices.Period{prices.Period24h}},
			wantMsg: "now",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := newServiceWithCanned(ChangeRepoResult{})
			_, err := svc.GetChange(context.Background(), c.q)
			if err == nil {
				t.Fatalf("expected error containing %q", c.wantMsg)
			}
			var ce *coreerrors.Error
			if !errors.As(err, &ce) {
				t.Fatalf("expected coreerrors.Error, got %T", err)
			}
			if ce.Code != coreerrors.CodeInvalidArgument {
				t.Errorf("code = %s, want INVALID_ARGUMENT", ce.Code)
			}
		})
	}
}

func TestChangeService_RepoErrorWrappedAsInternal(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	repo := &fakeChangeRepo{err: errors.New("db down")}
	svc := &ChangeService{Repo: repo, Cache: NewChangeCache(), Kind: "fa"}
	_, err := svc.GetChange(context.Background(), ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *coreerrors.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected coreerrors.Error, got %T", err)
	}
	if ce.Code != coreerrors.CodeInternal {
		t.Errorf("code = %s, want INTERNAL", ce.Code)
	}
}

func TestChangeService_Singleflight_CollapsesConcurrentRequests(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now:     []ChangeNow{{Currency: "usd", Price: decimal.RequireFromString("100"), TS: now, Found: true}},
		Anchors: []ChangeAnchor{{Currency: "usd", Period: prices.Period24h, Price: decimal.RequireFromString("90"), Bucket: now.Add(-24 * time.Hour), Found: true}},
	}
	repo := &fakeChangeRepo{canned: canned, delay: 50 * time.Millisecond}
	svc := &ChangeService{Repo: repo, Cache: NewChangeCache(), Kind: "fa"}
	q := ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.GetChange(context.Background(), q); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls := repo.Calls(); calls > 1 {
		t.Errorf("expected 1 repo call (singleflight collapses concurrent), got %d", calls)
	}
}

func TestChangeService_ComposeAsOfIsMaxNowTS(t *testing.T) {
	// Per Decision #7: top-level as_of = max(per-currency last_ts).
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	canned := ChangeRepoResult{
		Now: []ChangeNow{
			{Currency: "usd", Price: decimal.NewFromInt(1), TS: now.Add(-30 * time.Second), Found: true},
			{Currency: "eur", Price: decimal.NewFromInt(1), TS: now, Found: true}, // newest
			{Currency: "krw", Price: decimal.NewFromInt(1), TS: now.Add(-5 * time.Minute), Found: true},
		},
	}
	svc, _ := newServiceWithCanned(canned)
	res, err := svc.GetChange(context.Background(), ChangeQuery{
		Source:     prices.SourceCoinGecko,
		EntityKey:  "mvrk",
		Currencies: []string{"usd", "eur", "krw"},
		Periods:    []prices.Period{prices.Period24h},
		Now:        now,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.AsOf.Equal(now) {
		t.Errorf("AsOf = %v, want %v (max of per-currency now ts)", res.AsOf, now)
	}
}

func TestSingleflightKey_Stable(t *testing.T) {
	k1 := singleflightKey(prices.SourceCoinGecko, "mvrk", "", []string{"eur", "usd"}, []prices.Period{prices.Period24h, prices.Period7d})
	k2 := singleflightKey(prices.SourceCoinGecko, "mvrk", "", []string{"eur", "usd"}, []prices.Period{prices.Period24h, prices.Period7d})
	if k1 != k2 {
		t.Errorf("identical inputs must produce identical keys, got %q vs %q", k1, k2)
	}
	k3 := singleflightKey(prices.SourceCoinGecko, "mvrk", "", []string{"eur", "usd"}, []prices.Period{prices.Period7d, prices.Period24h})
	if k1 == k3 {
		t.Errorf("period order matters in key (caller must pre-sort) — got identical %q", k1)
	}
}

func TestMissingPeriodsOrderedByAllPeriods(t *testing.T) {
	// missingPeriods must order output by AllPeriods so SQL UNION branches
	// stay deterministic (helps EXPLAIN ANALYZE comparisons across CI runs).
	got := missingPeriods([]anchorKey{
		{Currency: "usd", Period: prices.Period30d},
		{Currency: "usd", Period: prices.Period1h},
		{Currency: "eur", Period: prices.Period1h}, // dedupes with previous
		{Currency: "usd", Period: prices.Period7d},
	})
	want := []prices.Period{prices.Period1h, prices.Period7d, prices.Period30d}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, p := range want {
		if got[i] != p {
			t.Errorf("[%d] = %q, want %q (full got=%v)", i, got[i], p, got)
		}
	}
}

func TestClassifyError_ClosedEnum(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{coreerrors.InvalidArgument("unsupported period: 12h"), "invalid_period"},
		{coreerrors.InvalidArgument("Invalid 'currency' value: zzz"), "invalid_currency"},
		{coreerrors.InvalidArgument("entity is required"), "invalid_argument"},
		{coreerrors.NotFound("token"), "not_found"},
		{coreerrors.Internal("oops", nil), "internal"},
		{errors.New("plain"), "internal"},
	}
	for _, c := range cases {
		if got := classifyError(c.err); got != c.want {
			t.Errorf("classifyError(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
