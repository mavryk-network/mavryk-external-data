// Price-change endpoint application layer, shaped like charts.go: one service
// over one opaque repository, with EntityKey neutral so FT and RWA share it.
package prices

import (
	"context"
	"sort"
	"strings"
	"time"

	coreerrors "quotes/internal/core/common/errors"
	"quotes/internal/core/domain/prices"
	"quotes/internal/metrics"

	"github.com/shopspring/decimal"
	"golang.org/x/sync/singleflight"
)

// changeRepoTimeout bounds the detached singleflight repo call, so a stuck DB
// connection cannot pin the flight forever.
const changeRepoTimeout = 15 * time.Second

// changePctDivPrecision is wider than decimal's default (16) so a tiny-but-real
// move cannot round to a flat 0%.
const changePctDivPrecision = 24

// ChangeQuery fetches price-change anchors. The repository interprets the keys:
//
//	FT  repo: EntityKey = token_symbol, AuxKey unused.
//	RWA repo: EntityKey = pair_id (decimal string), AuxKey = side ("last").
//
// Currencies are opaque here (FT validates the closed enum at the HTTP
// boundary; for RWA the field is metadata only). Now is pinned once per
// request so retries and cache lookups stay coherent.
type ChangeQuery struct {
	Source     prices.Source
	EntityKey  string
	AuxKey     string
	Currencies []string
	Periods    []prices.Period
	Now        time.Time
}

// ChangeAnchor is one (currency, period) anchor row from the repository.
// Found=false means no row matched the period's tolerance window — the
// service maps that to a JSON null in the response.
type ChangeAnchor struct {
	Currency string
	Period   prices.Period
	Price    decimal.Decimal
	Bucket   time.Time
	Found    bool
}

// ChangeNow is the latest sample for one currency; Found=false means no
// observations yet.
type ChangeNow struct {
	Currency string
	Price    decimal.Decimal
	TS       time.Time
	Found    bool
}

// ChangeRepoResult is the raw repository output the service composes
// ChangeResult from.
type ChangeRepoResult struct {
	Now     []ChangeNow
	Anchors []ChangeAnchor
}

// ChangeRepository is the storage contract behind ChangeService, satisfied by
// the FT and RWA implementations. One SQL per request: the implementation
// assembles the UNION-of-CAs query.
type ChangeRepository interface {
	GetChange(ctx context.Context, q ChangeQuery) (ChangeRepoResult, error)
}

// ChangeResult mirrors the /change JSON shape in domain types, so only the HTTP
// layer knows about num6 / RFC3339.
type ChangeResult struct {
	AsOf       time.Time
	Currencies map[string]ChangeForCurrency
}

// ChangeForCurrency is the per-currency block; NowFound=false means no latest
// sample yet (a newly-registered token before the live job fills it in).
type ChangeForCurrency struct {
	Now      decimal.Decimal
	NowTS    time.Time
	NowFound bool
	ByPeriod map[prices.Period]ChangeForPeriod
}

// ChangeForPeriod is one anchor's contribution. AnchorFound=false renders JSON
// null; ChangePctValid=false is the divide-by-zero edge case, where from_ts and
// from_price stay populated but delta_abs and change_pct are null.
type ChangeForPeriod struct {
	FromPrice      decimal.Decimal
	FromTS         time.Time
	AnchorFound    bool
	DeltaAbs       decimal.Decimal
	ChangePct      decimal.Decimal
	ChangePctValid bool
}

// ChangeService composes repository, cache, metrics and singleflight stampede
// protection. Kind ("fa"/"rwa") labels the Prometheus metrics.
type ChangeService struct {
	Repo  ChangeRepository
	Cache *ChangeCache
	Kind  string

	sf singleflight.Group
}

// GetChange returns the price-change response for one request: validate, read
// the cache, fetch the misses through singleflight, then compose the result.
func (s *ChangeService) GetChange(ctx context.Context, q ChangeQuery) (ChangeResult, error) {
	defer s.observeDuration(len(q.Periods), time.Now())

	if err := s.preflight(q); err != nil {
		s.observeError(classifyError(err))
		return ChangeResult{}, err
	}

	missingNow, missingAnchor := s.collectMisses(q)

	if len(missingNow) > 0 || len(missingAnchor) > 0 {
		repoQ := ChangeQuery{
			Source:     q.Source,
			EntityKey:  q.EntityKey,
			AuxKey:     q.AuxKey,
			Currencies: missingCurrencies(missingNow, missingAnchor),
			Periods:    missingPeriods(missingAnchor),
			Now:        q.Now,
		}
		sfKey := singleflightKey(q.Source, q.EntityKey, q.AuxKey, repoQ.Currencies, repoQ.Periods)
		v, err, shared := s.sf.Do(sfKey, func() (any, error) {
			// Detached from the leader's ctx: its disconnect would otherwise
			// cancel the shared query and 500 every waiter. WithoutCancel keeps
			// the request id; changeRepoTimeout bounds it instead.
			callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), changeRepoTimeout)
			defer cancel()
			return s.Repo.GetChange(callCtx, repoQ)
		})
		if shared {
			s.observeCache("singleflight_collapsed")
		}
		if err != nil {
			s.observeError("repo_error")
			return ChangeResult{}, coreerrors.Internal("change query failed", err)
		}
		repoRes, _ := v.(ChangeRepoResult)
		s.populateCache(q, repoRes)
	}

	return s.compose(q), nil
}

func (s *ChangeService) preflight(q ChangeQuery) error {
	if q.Source == "" {
		return coreerrors.InvalidArgument("source is required")
	}
	if q.EntityKey == "" {
		return coreerrors.InvalidArgument("entity is required")
	}
	if len(q.Currencies) == 0 {
		return coreerrors.InvalidArgument("at least one currency is required")
	}
	if len(q.Periods) == 0 {
		return coreerrors.InvalidArgument("at least one period is required")
	}
	for _, p := range q.Periods {
		if _, ok := p.Duration(); !ok {
			return coreerrors.InvalidArgument("unsupported period: " + string(p))
		}
	}
	if q.Now.IsZero() {
		return coreerrors.InvalidArgument("now is required")
	}
	return nil
}

// collectMisses returns the now/anchor slots that still need a repo fetch.
func (s *ChangeService) collectMisses(q ChangeQuery) (missingNow []string, missingAnchor []anchorKey) {
	for _, cur := range q.Currencies {
		if _, _, ok := s.Cache.GetNow(q.Source, q.EntityKey, q.AuxKey, cur); ok {
			s.observeCache("hit")
		} else {
			s.observeCache("miss")
			missingNow = append(missingNow, cur)
		}
		for _, p := range q.Periods {
			if _, _, ok := s.Cache.GetAnchor(q.Source, q.EntityKey, q.AuxKey, cur, p); ok {
				s.observeCache("hit")
			} else {
				s.observeCache("miss")
				missingAnchor = append(missingAnchor, anchorKey{Currency: cur, Period: p})
			}
		}
	}
	return missingNow, missingAnchor
}

// populateCache writes repo results into the cache; "not found" is never cached.
func (s *ChangeService) populateCache(q ChangeQuery, res ChangeRepoResult) {
	for _, n := range res.Now {
		if !n.Found {
			continue
		}
		s.Cache.SetNow(q.Source, q.EntityKey, q.AuxKey, n.Currency, n.Price, n.TS)
	}
	for _, a := range res.Anchors {
		if !a.Found {
			continue
		}
		s.Cache.SetAnchor(q.Source, q.EntityKey, q.AuxKey, a.Currency, a.Period, a.Price, a.Bucket)
	}
}

// compose assembles the final ChangeResult from the cache.
func (s *ChangeService) compose(q ChangeQuery) ChangeResult {
	currencies := make(map[string]ChangeForCurrency, len(q.Currencies))
	var newest time.Time
	for _, cur := range q.Currencies {
		nowPrice, nowTS, nowOK := s.Cache.GetNow(q.Source, q.EntityKey, q.AuxKey, cur)
		byPeriod := make(map[prices.Period]ChangeForPeriod, len(q.Periods))
		for _, p := range q.Periods {
			anchorPrice, anchorTS, anchorOK := s.Cache.GetAnchor(q.Source, q.EntityKey, q.AuxKey, cur, p)
			cfp := ChangeForPeriod{}
			if anchorOK {
				cfp.AnchorFound = true
				cfp.FromPrice = anchorPrice
				cfp.FromTS = anchorTS
				if nowOK && !anchorPrice.IsZero() {
					cfp.DeltaAbs = nowPrice.Sub(anchorPrice)
					// Multiply first, and carry more than Div's default 16 dp:
					// rounding the raw ratio there can zero change_pct while
					// delta_abs survives.
					cfp.ChangePct = cfp.DeltaAbs.
						Mul(decimal.NewFromInt(100)).
						DivRound(anchorPrice, changePctDivPrecision)
					cfp.ChangePctValid = true
				}
				// If anchorPrice.IsZero() the period stays AnchorFound=true
				// with valid FromPrice/FromTS, but ChangePctValid=false —
				// per Decision #10 (div-by-zero handling).
			}
			byPeriod[p] = cfp
		}
		currencies[cur] = ChangeForCurrency{
			Now:      nowPrice,
			NowTS:    nowTS,
			NowFound: nowOK,
			ByPeriod: byPeriod,
		}
		if nowOK && nowTS.After(newest) {
			newest = nowTS
		}
	}
	return ChangeResult{AsOf: newest, Currencies: currencies}
}

// --- helpers ---

type anchorKey struct {
	Currency string
	Period   prices.Period
}

// missingCurrencies dedupes the currencies across both miss lists.
func missingCurrencies(missingNow []string, missingAnchor []anchorKey) []string {
	seen := make(map[string]struct{}, len(missingNow)+len(missingAnchor))
	for _, c := range missingNow {
		seen[c] = struct{}{}
	}
	for _, k := range missingAnchor {
		seen[k.Currency] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// missingPeriods dedupes periods, ordered by AllPeriods so the SQL UNION
// branches stay deterministic.
func missingPeriods(missingAnchor []anchorKey) []prices.Period {
	seen := make(map[prices.Period]struct{}, len(missingAnchor))
	for _, k := range missingAnchor {
		seen[k.Period] = struct{}{}
	}
	out := make([]prices.Period, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return periodOrdinal(out[i]) < periodOrdinal(out[j])
	})
	return out
}

func periodOrdinal(p prices.Period) int {
	for i, v := range prices.AllPeriods {
		if v == p {
			return i
		}
	}
	return -1
}

// singleflightKey builds a stable key for the singleflight group. Format:
// "<source>|<entity>|<aux>|<curs sorted>|<periods sorted>". Currencies/periods
// are already sorted by missingCurrencies/missingPeriods.
func singleflightKey(source prices.Source, entity, aux string, curs []string, periods []prices.Period) string {
	parts := []string{string(source), entity, aux, strings.Join(curs, ",")}
	pstrs := make([]string, len(periods))
	for i, p := range periods {
		pstrs[i] = string(p)
	}
	parts = append(parts, strings.Join(pstrs, ","))
	return strings.Join(parts, "|")
}

// classifyError maps an error returned by preflight / repo into the
// closed metric label enum. Labels are bounded (no user-input echo)
// so cardinality stays predictable.
func classifyError(err error) string {
	if err == nil {
		return "ok"
	}
	if ce, ok := err.(*coreerrors.Error); ok {
		switch ce.Code {
		case coreerrors.CodeInvalidArgument:
			msg := strings.ToLower(ce.Message)
			switch {
			case strings.Contains(msg, "period"):
				return "invalid_period"
			case strings.Contains(msg, "currency"):
				return "invalid_currency"
			default:
				return "invalid_argument"
			}
		case coreerrors.CodeNotFound:
			return "not_found"
		case coreerrors.CodeInternal:
			return "internal"
		default:
			return "internal"
		}
	}
	return "internal"
}

// --- metric helpers ---

func (s *ChangeService) kindLabel() string {
	if s.Kind == "" {
		return "unknown"
	}
	return s.Kind
}

func (s *ChangeService) observeDuration(periodsCount int, start time.Time) {
	metrics.ChangeQueryDurationSeconds.
		WithLabelValues(s.kindLabel(), itoa(periodsCount)).
		Observe(time.Since(start).Seconds())
}

func (s *ChangeService) observeCache(result string) {
	metrics.ChangeQueryCacheHitsTotal.
		WithLabelValues(s.kindLabel(), result).
		Inc()
}

func (s *ChangeService) observeError(code string) {
	metrics.ChangeQueryErrorsTotal.
		WithLabelValues(s.kindLabel(), code).
		Inc()
}

// itoa is the fast int→string for small positive ints (period count is 1..4).
// Avoids strconv for one tiny call site; values are always bounded.
func itoa(n int) string {
	if n < 0 || n > 99 {
		return "other"
	}
	if n < 10 {
		return string('0' + byte(n))
	}
	return string('0'+byte(n/10)) + string('0'+byte(n%10))
}
