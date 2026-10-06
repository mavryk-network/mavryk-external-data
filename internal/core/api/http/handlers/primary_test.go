package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	app "quotes/internal/core/application/prices"
)

func TestPrimaryHandler_UnknownIsExplicitAndInputIsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/primary", (PrimaryDeps{}).Evaluate())
	item := app.PrimaryRequest{Network: "basenet", Contract: "KT1HR56nVuVDnidsabd2TBB9YvAtKbCbHKpP", TokenID: "0", At: time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC), Level: 123}
	for _, tc := range []struct {
		name    string
		payload any
		status  int
	}{
		{"unknown", primaryBatchRequest{Requests: []app.PrimaryRequest{item}}, http.StatusOK},
		{"empty", primaryBatchRequest{}, http.StatusBadRequest},
		{"oversized", primaryBatchRequest{Requests: make([]app.PrimaryRequest, 257)}, http.StatusBadRequest},
		{"missing_identity", primaryBatchRequest{Requests: []app.PrimaryRequest{{At: item.At, Level: 123}}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.payload)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/primary", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.status == http.StatusOK {
				var got primaryBatchResponse
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Results) != 1 || got.Results[0].USD != nil || got.Results[0].Reason != "history_unavailable" || got.Results[0].Level != item.Level {
					t.Fatalf("got %+v", got)
				}
			}
		})
	}
}
