package common

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func ctxWithQuery(rawQuery string) *gin.Context {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/x?"+rawQuery, nil)
	return c
}

func TestBindPriceQuery_WindowWithoutLimit_ClampsToMax(t *testing.T) {
	// Regression (unauthenticated DoS): a window query with no ?limit must be
	// capped at MaxLimit. Before the fix q.Limit stayed 0 → repos added no SQL
	// LIMIT → a from=epoch scan materialized millions of rows in memory.
	c := ctxWithQuery("from=1970-01-01T00:00:00Z")
	q, err := BindPriceQuery(c, QueryOptions{DefaultLatestLimit: 100, MaxLimit: 10000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Limit != 10000 {
		t.Errorf("window query without ?limit: Limit = %d, want 10000 (MaxLimit clamp)", q.Limit)
	}
}

func TestBindPriceQuery_WindowWithExplicitLimit_Kept(t *testing.T) {
	c := ctxWithQuery("from=2026-01-01T00:00:00Z&limit=250")
	q, err := BindPriceQuery(c, QueryOptions{DefaultLatestLimit: 100, MaxLimit: 10000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Limit != 250 {
		t.Errorf("Limit = %d, want 250 (explicit limit under cap is kept)", q.Limit)
	}
}

func TestBindPriceQuery_WindowOverCap_Rejected(t *testing.T) {
	c := ctxWithQuery("from=2026-01-01T00:00:00Z&limit=99999")
	if _, err := BindPriceQuery(c, QueryOptions{DefaultLatestLimit: 100, MaxLimit: 10000}); err == nil {
		t.Fatal("expected 400 for limit over MaxLimit, got nil")
	}
}

func TestBindPriceQuery_LatestMode_UsesDefault(t *testing.T) {
	c := ctxWithQuery("")
	q, err := BindPriceQuery(c, QueryOptions{DefaultLatestLimit: 100, MaxLimit: 10000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Limit != 100 {
		t.Errorf("latest mode: Limit = %d, want 100 (DefaultLatestLimit)", q.Limit)
	}
}

func TestBindPriceQuery_WindowNoLimit_CapDisabled_StaysUnbounded(t *testing.T) {
	// MaxLimit == 0 means the operator explicitly disabled the cap; we honor it
	// (no clamp) rather than inventing a bound.
	c := ctxWithQuery("from=2026-01-01T00:00:00Z")
	q, err := BindPriceQuery(c, QueryOptions{DefaultLatestLimit: 100, MaxLimit: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Limit != 0 {
		t.Errorf("Limit = %d, want 0 (cap disabled → no clamp)", q.Limit)
	}
}
