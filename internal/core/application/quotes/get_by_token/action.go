package get_by_token

import (
	"context"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/responsecache"
	"strings"
	"time"
)

type Repository interface {
	GetQuotes(ctx context.Context, from, to time.Time, limit int, tokenName string) ([]quotes.Quote, error)
}

type Action struct {
	repo  Repository
	cache *responsecache.Cache
}

func New(repo Repository, cache *responsecache.Cache) *Action {
	return &Action{repo: repo, cache: cache}
}

func (a *Action) Execute(ctx context.Context, tokenName string, from, to time.Time, limit int) ([]quotes.Quote, error) {
	if a.cache != nil && from.IsZero() && to.IsZero() && limit > 0 {
		if list, ok := a.cache.GetLatestList(tokenName, limit); ok {
			return list, nil
		}
	}
	quotes, err := a.repo.GetQuotes(ctx, from, to, limit, tokenName)
	if err != nil {
		// Check if error is about unsupported token
		if strings.Contains(err.Error(), "not supported") {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}

	if a.cache != nil && from.IsZero() && to.IsZero() && limit > 0 {
		a.cache.SetLatestList(tokenName, limit, quotes)
	}
	return quotes, nil
}
