package get_latest

import (
	"context"
	"quotes/internal/core/domain/quotes"
)

type Repository interface {
	GetLastQuote(ctx context.Context) (quotes.Quote, error)
}

type Action struct {
	repo Repository
}

func New(repo Repository) *Action {
	return &Action{repo: repo}
}

func (a *Action) Execute(ctx context.Context) (quotes.Quote, error) {
	return a.repo.GetLastQuote(ctx)
}
