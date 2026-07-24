// Price-change endpoint application layer. Mirrors charts.go in shape:
// one ChangeService, one opaque ChangeRepository, EntityKey kept neutral
// so FT and RWA share the same service. Source is explicit (not packed
// into AuxKey like charts.go) because /change has a single source per
// class — no need to encode it in a side-channel string.
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

// changeRepoTimeout bounds the detached singleflight repo call (see GetChange).
// Generous relative to a single indexed-seek query, but finite so a stuck DB
// connection cannot pin the flight forever.
const changeRepoTimeout = 15 * time.Second

// ChangeQuery is the application-layer parameter object for fetching
// price-change anchors. The repository decides how to interpret EntityKey
// and AuxKey:
//
//	FT  repo: EntityKey = token_symbol, AuxKey is unused.
//	RWA repo: EntityKey = pair_id (decimal string), AuxKey = side ("last").
//
// Currencies are opaque strings at this layer: FT validates against the
// closed Currency enum on the HTTP boundary, RWA passes the pair's native
// quote ticker (open set, e.g. "usdt"). The repo decides what to do with
// each currency — for RWA the field is metadata only (rwa_quote_prices
// CAs are not keyed by currency).
//
// Now is the wall-clock anchor against which `period` distances are
// measured. Service callers pin it once per request so retries / cache
// lookups stay coherent.
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

// ChangeNow is the latest sample for one currency. Mirrors LatestSnapshot
// semantics (last(price) per currency); Found=false means no observations
// for that currency yet.
type ChangeNow struct {
	Currency string
	Price    decimal.Decimal
	TS       time.Time
	Found    bool
}

// ChangeRepoResult is what ChangeRepository.GetChange returns. The
// service composes the user-visible ChangeResult from this raw form
// plus per-period cache lookups.
type ChangeRepoResult struct {
	Now     []ChangeNow
	Anchors []ChangeAnchor
}

// ChangeRepository is the storage contract used by ChangeService. Both
// the FT (token_change_repository) and RWA (rwa_change_repository)
// concrete impls satisfy it. One SQL per request — the implementation
// is responsible for assembling the UNION-of-CAs query.
type ChangeRepository interface {
	GetChange(ctx context.Context, q ChangeQuery) (ChangeRepoResult, error)
}

// ChangeResult is the service-layer view of a /change response. Mirrors
// the on-wire JSON shape but stays in domain types (decimal, time.Time)
// so the HTTP layer is the only place that knows about num6 / RFC3339.
type ChangeResult struct {
	AsOf       time.Time
	Currencies map[string]ChangeForCurrency
}

// ChangeForCurrency is the per-currency block. NowFound=false flags an
// absent latest sample (very rare; happens for newly-registered tokens
// before the live job has filled in the currency).
type ChangeForCurrency struct {
	Now      decimal.Decimal
	NowTS    time.Time
	NowFound bool
	ByPeriod map[prices.Period]ChangeForPeriod
}

// ChangeForPeriod is one anchor's contribution. AnchorFound=false maps
// to JSON null on the wire (Decision #3 from /office-hours premises).
// ChangePctValid=false additionally signals the divide-by-zero edge
// case (Decision #10): from_ts and from_price stay populated, but the
// computed delta_abs and change_pct are null.
type ChangeForPeriod struct {
	FromPrice      decimal.Decimal
	FromTS         time.Time
	AnchorFound    bool
	DeltaAbs       decimal.Decimal
	ChangePct      decimal.Decimal
	ChangePctValid bool
}

// ChangeService composes ChangeRepository + ChangeCache + metrics +
// singleflight stampede protection. Kind labels Prometheus metrics —
// "fa" or "rwa", per existing ChartService convention.
type ChangeService struct {
	Repo  ChangeRepository
	Cache *ChangeCache
	Kind  string

	sf singleflight.Group
}

// GetChange returns the price-change response for one (entity, currencies, periods)
// request. The flow:
//
//  1. Validate the request (closed-enum periods, non-empty currencies).
//  2. For each (currency) and (currency, period), check the cache. Keep
//     a list of cache misses.
//  3. If any miss → call repo.GetChange(ctx) for the misses, under
//     singleflight to collapse duplicate concurrent requests for the same key.
//  4. Fill the cache with the new rows.
//  5. Compose ChangeResult: for each (currency, period), compute delta_abs
//     and change_pct (or mark them null on missing anchor / zero anchor).
func (s *ChangeService) GetChange(ctx context.Context, q ChangeQuery) (ChangeResult, error) {
	defer s.observeDuration(len(q.Periods), time.Now())

	if err := s.preflight(q); err != nil {
		s.observeError(classifyError(err))
		return ChangeResult{}, err
	}

	// Step 2: cache lookup for every (currency) "now" + every (currency, period) anchor.
	missingNow, missingAnchor := s.collectMisses(q)

	// Step 3: fetch misses via repo (with singleflight stampede protection).
	if len(missingNow) > 0 || len(missingAnchor) > 0 {
		repoQ := ChangeQuery{
			Source:     q.Source,
			EntityKey:  q.EntityKey,
			AuxKey:     q.AuxKey,
			Currencies: missingCurrencies(missingNow, missingAnchor),
			Periods:    missingPeriods(missingAnchor),
			Now:        q.Now,
		}
		// Singleflight key: stable string from the missing tuples.
		// Two concurrent identical requests (same key, same set of misses)
		// share one repo round-trip.
		sfKey := singleflightKey(q.Source, q.EntityKey, q.AuxKey, repoQ.Currencies, repoQ.Periods)
		v, err, shared := s.sf.Do(sfKey, func() (any, error) {
			// Detach from the leader's request context. singleflight collapses N
			// concurrent callers onto one repo call; if that call ran on the
			// leader's ctx, the leader disconnecting (mobile nav-away, LB timeout)
			// would cancel the shared query and fail every waiter with a 500 even
			// though their own clients are still connected. WithoutCancel keeps
			// ctx values (request id) but drops cancellation; we bound it with our
			// own timeout instead.
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

	// Step 5: compose the response from the cache (now fully populated for
	// found rows; misses for absent anchors stay AnchorFound=false).
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

// collectMisses scans the cache for every (currency) now + (currency, period)
// anchor. Returned slices identify which still need a repo fetch.
func (s *ChangeService) collectMisses(q ChangeQuery) (missingNow []string, missingAnchor []anchorKey) {
	for _, cur := range q.Currencies {
		if _, _, ok := s.Cache.GetNow(q.Source, q.EntityKey, cur); ok {
			s.observeCache("hit")
		} else {
			s.observeCache("miss")
			missingNow = append(missingNow, cur)
		}
		for _, p := range q.Periods {
			if _, _, ok := s.Cache.GetAnchor(q.Source, q.EntityKey, cur, p); ok {
				s.observeCache("hit")
			} else {
				s.observeCache("miss")
				missingAnchor = append(missingAnchor, anchorKey{Currency: cur, Period: p})
			}
		}
	}
	return missingNow, missingAnchor
}

// populateCache writes repo results into the cache. Rows the repo did
// not return (Found=false) are skipped — we never cache "not found".
func (s *ChangeService) populateCache(q ChangeQuery, res ChangeRepoResult) {
	for _, n := range res.Now {
		if !n.Found {
			continue
		}
		s.Cache.SetNow(q.Source, q.EntityKey, n.Currency, n.Price, n.TS)
	}
	for _, a := range res.Anchors {
		if !a.Found {
			continue
		}
		s.Cache.SetAnchor(q.Source, q.EntityKey, a.Currency, a.Period, a.Price, a.Bucket)
	}
}

// compose assembles the final ChangeResult from the now-fully-populated cache
// (or partially-populated, for currencies with no live data yet).
func (s *ChangeService) compose(q ChangeQuery) ChangeResult {
	currencies := make(map[string]ChangeForCurrency, len(q.Currencies))
	var newest time.Time
	for _, cur := range q.Currencies {
		nowPrice, nowTS, nowOK := s.Cache.GetNow(q.Source, q.EntityKey, cur)
		byPeriod := make(map[prices.Period]ChangeForPeriod, len(q.Periods))
		for _, p := range q.Periods {
			anchorPrice, anchorTS, anchorOK := s.Cache.GetAnchor(q.Source, q.EntityKey, cur, p)
			cfp := ChangeForPeriod{}
			if anchorOK {
				cfp.AnchorFound = true
				cfp.FromPrice = anchorPrice
				cfp.FromTS = anchorTS
				if nowOK && !anchorPrice.IsZero() {
					cfp.DeltaAbs = nowPrice.Sub(anchorPrice)
					cfp.ChangePct = cfp.DeltaAbs.
						Div(anchorPrice).
						Mul(decimal.NewFromInt(100))
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

// missingCurrencies returns the union of currencies appearing in either
// missingNow or missingAnchor (deduplicated). The repo only needs each
// currency once per scan.
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

// missingPeriods returns the union of periods appearing in missingAnchor,
// ordered by AllPeriods so SQL UNION branches stay deterministic.
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
