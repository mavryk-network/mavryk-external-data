package get_count

import (
	"context"
)

type Repository interface {
	GetCount(ctx context.Context) (int64, error)
}

type Action struct {
	repo Repository
}

func New(repo Repository) *Action {
	return &Action{repo: repo}
}

func (a *Action) Execute(ctx context.Context) (int64, error) {
	return a.repo.GetCount(ctx)
}
