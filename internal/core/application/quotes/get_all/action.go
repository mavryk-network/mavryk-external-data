package get_all

import (
	"context"
	"quotes/internal/core/domain/quotes"
	"time"
)

type Repository interface {
	GetQuotes(ctx context.Context, from, to time.Time, limit int) ([]quotes.Quote, error)
}

type Action struct {
	repo Repository
}

func New(repo Repository) *Action {
	return &Action{repo: repo}
}

func (a *Action) Execute(ctx context.Context, from, to time.Time, limit int) ([]quotes.Quote, error) {
	return a.repo.GetQuotes(ctx, from, to, limit)
}
