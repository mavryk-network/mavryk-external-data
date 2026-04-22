package get_latest

import (
	"context"
	"quotes/internal/core/domain/quotes"
	"quotes/internal/core/infrastructure/responsecache"
)

type Repository interface {
	GetLastQuote(ctx context.Context, tokenName string) (quotes.Quote, error)
}

type Action struct {
	repo  Repository
	cache *responsecache.Cache
}

func New(repo Repository, cache *responsecache.Cache) *Action {
	return &Action{repo: repo, cache: cache}
}

func (a *Action) Execute(ctx context.Context, tokenName string) (quotes.Quote, error) {
	if a.cache != nil {
		if q, ok := a.cache.GetLastQuote(tokenName); ok {
			return q, nil
		}
	}
	q, err := a.repo.GetLastQuote(ctx, tokenName)
	if err != nil || a.cache == nil {
		return q, err
	}
	a.cache.SetLastQuote(tokenName, q)
	return q, nil
}
