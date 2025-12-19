package get_by_token

import (
	"context"
	"quotes/internal/core/domain/quotes"
	"strings"
	"time"
)

type Repository interface {
	GetQuotes(ctx context.Context, from, to time.Time, limit int, tokenName string) ([]quotes.Quote, error)
}

type Action struct {
	repo Repository
}

func New(repo Repository) *Action {
	return &Action{repo: repo}
}

func (a *Action) Execute(ctx context.Context, tokenName string, from, to time.Time, limit int) ([]quotes.Quote, error) {
	quotes, err := a.repo.GetQuotes(ctx, from, to, limit, tokenName)
	if err != nil {
		// Check if error is about unsupported token
		if strings.Contains(err.Error(), "not supported") {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}

	return quotes, nil
}

