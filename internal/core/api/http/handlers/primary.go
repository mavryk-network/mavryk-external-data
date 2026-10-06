package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quotes/internal/core/api/http/common"
	app "quotes/internal/core/application/prices"
	errors "quotes/internal/core/common/errors"
	"quotes/internal/metrics"

	"github.com/gin-gonic/gin"
)

type PrimaryDeps struct{ Service *app.PrimaryValuator }
type primaryBatchRequest struct {
	Requests []app.PrimaryRequest `json:"requests"`
}
type primaryBatchResponse struct {
	Results []app.PrimaryResult `json:"results"`
}

func (d PrimaryDeps) Evaluate() gin.HandlerFunc {
	bind := func(c *gin.Context) (primaryBatchRequest, error) {
		var req primaryBatchRequest
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return req, errors.InvalidArgument("invalid primary valuation request")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return req, errors.InvalidArgument("expected one JSON object")
		}
		if len(req.Requests) == 0 || len(req.Requests) > app.PrimaryBatchLimit {
			return req, errors.InvalidArgument("requests must contain 1-256 items")
		}
		for _, item := range req.Requests {
			id, err := strconv.ParseUint(item.TokenID, 10, 64)
			if err != nil || strconv.FormatUint(id, 10) != item.TokenID || item.Network == "" || len(item.Contract) != 36 || !strings.HasPrefix(item.Contract, "KT1") || strings.ContainsAny(item.Contract, "/:? \t\n") || item.At.IsZero() || item.At.After(time.Now().Add(time.Minute)) || item.Level <= 0 {
				return req, errors.InvalidArgument("each item requires network, exact token identity, at and level")
			}
		}
		return req, nil
	}
	action := func(ctx context.Context, req primaryBatchRequest) (primaryBatchResponse, error) {
		results := d.Service.Evaluate(ctx, req.Requests)
		for _, result := range results {
			reason := result.Reason
			if reason == "" {
				reason = "ok"
			}
			metrics.PrimaryValuationsTotal.WithLabelValues(reason).Inc()
		}
		return primaryBatchResponse{Results: results}, nil
	}
	return common.Wrap(bind, action)
}
