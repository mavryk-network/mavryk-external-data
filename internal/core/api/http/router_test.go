package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSetupRoutes_RWAOverviewNoConflict proves GET /v1/rwa (the overview list,
// registered on the /rwa group root) coexists with /v1/rwa/:symbol without a
// gin radix-tree conflict. Zero-value deps are fine: SetupRoutes only registers
// handler closures, it never invokes them, so nil services never run here.
func TestSetupRoutes_RWAOverviewNoConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetupRoutes panicked (route conflict?): %v", r)
		}
	}()
	SetupRoutes(engine, RouterDeps{})

	var hasOverview, hasSymbol bool
	for _, ri := range engine.Routes() {
		if ri.Method != "GET" {
			continue
		}
		switch ri.Path {
		case "/v1/rwa":
			hasOverview = true
		case "/v1/rwa/:symbol":
			hasSymbol = true
		}
	}
	if !hasOverview {
		t.Error("GET /v1/rwa (overview) is not registered")
	}
	if !hasSymbol {
		t.Error("GET /v1/rwa/:symbol is not registered (sanity check)")
	}
}

func TestSetupRoutes_PrimaryInheritsRWAAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetupRoutes(engine, RouterDeps{RWAAuth: func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	}})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/valuations/primary", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("primary route skipped RWA auth: status %d", recorder.Code)
	}
}
