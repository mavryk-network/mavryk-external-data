package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"quotes/internal/core/domain/prices"

	"github.com/gin-gonic/gin"
)

func newRWAEngine(_ *testing.T, deps RWAPriceDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/rwa/:pair_id", deps.ListByPair())
	r.GET("/v1/rwa/:pair_id/latest", deps.LatestByPair())
	return r
}

func TestRWA_NonNumericPairID_400(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/abc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRWA_NegativePairID_400(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for pair_id=0", w.Code)
	}
}

func TestRWA_BadSide_400(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceEquiteez,
		MaxLimit:      1000,
	}
	r := newRWAEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/123?side=foo", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for unknown side", w.Code)
	}
}

func TestRWA_LatestNoData_404(t *testing.T) {
	deps := RWAPriceDeps{
		Service:       &stubQueryService{},
		DefaultSource: prices.SourceEquiteez,
	}
	r := newRWAEngine(t, deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/rwa/123/latest", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
