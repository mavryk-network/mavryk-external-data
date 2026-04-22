package responsecache

import (
	"quotes/internal/core/domain/quotes"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cache is an in-process TTL cache for hot read paths (latest single quote, latest quote lists).
// It is not shared across replicas; use Redis if you need a distributed cache.
type Cache struct {
	mu   sync.RWMutex
	ttl  time.Duration
	last map[string]entryQuote
	list map[string]entryQuotes
}

type entryQuote struct {
	v   quotes.Quote
	exp time.Time
}

type entryQuotes struct {
	v   []quotes.Quote
	exp time.Time
}

func New(ttl time.Duration) *Cache {
	return &Cache{
		ttl:  ttl,
		last: make(map[string]entryQuote),
		list: make(map[string]entryQuotes),
	}
}

func normalizeToken(t string) string {
	return strings.ToLower(strings.TrimSpace(t))
}

func listCacheKey(token string, limit int) string {
	return normalizeToken(token) + "|" + strconv.Itoa(limit)
}

func (c *Cache) GetLastQuote(token string) (quotes.Quote, bool) {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := normalizeToken(token)
	e, ok := c.last[key]
	if !ok || now.After(e.exp) {
		return quotes.Quote{}, false
	}
	return e.v, true
}

func (c *Cache) SetLastQuote(token string, q quotes.Quote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := normalizeToken(token)
	c.last[key] = entryQuote{v: q, exp: time.Now().Add(c.ttl)}
}

func (c *Cache) GetLatestList(token string, limit int) ([]quotes.Quote, bool) {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	key := listCacheKey(token, limit)
	e, ok := c.list[key]
	if !ok || now.After(e.exp) {
		return nil, false
	}
	return slices.Clone(e.v), true
}

func (c *Cache) SetLatestList(token string, limit int, list []quotes.Quote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := listCacheKey(token, limit)
	c.list[key] = entryQuotes{v: slices.Clone(list), exp: time.Now().Add(c.ttl)}
}

// InvalidateToken drops all cached entries for the token (single latest + all list limits).
func (c *Cache) InvalidateToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tok := normalizeToken(token)
	delete(c.last, tok)
	prefix := tok + "|"
	for k := range c.list {
		if strings.HasPrefix(k, prefix) {
			delete(c.list, k)
		}
	}
}
