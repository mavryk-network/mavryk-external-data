package get_all

import (
	"net/http"
	"quotes/internal/core/application/quotes/get_all"
	domainQuotes "quotes/internal/core/domain/quotes"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_all.Action
}

func New(action *get_all.Action) *Handler {
	return &Handler{action: action}
}

// GetQuotes godoc
// @Summary      Get quotes for MVRK token (legacy endpoint)
// @Description  Retrieve quotes for MVRK token with optional filters. Returns quotes within the specified time range.
// @Tags         quotes
// @Accept       json
// @Produce      json
// @Param        from    query     string  false  "Start time (RFC3339 format, e.g., 2025-01-01T00:00:00Z). Default: 24 hours ago"
// @Param        to      query     string  false  "End time (RFC3339 format, e.g., 2025-01-01T23:59:59Z). Default: now"
// @Param        limit   query     int     false  "Maximum number of quotes to return. Default: no limit"
// @Success      200     {array}   quotes.Quote  "List of quotes"
// @Failure      400     {object}  map[string]string  "Invalid request parameters"
// @Failure      500     {object}  map[string]string  "Internal server error"
// @Router       /quotes [get]
func (h *Handler) Handle(c *gin.Context) {
	fromStr := c.Query("from")
	toStr := c.Query("to")
	limitStr := c.Query("limit")

	now := time.Now()
	from := now.Add(-24 * time.Hour)
	to := now

	if fromStr != "" {
		if parsedFrom, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = parsedFrom
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid 'from' parameter format. Use RFC3339 format (e.g., 2023-01-01T00:00:00Z)",
			})
			return
		}
	}

	if toStr != "" {
		if parsedTo, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = parsedTo
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid 'to' parameter format. Use RFC3339 format (e.g., 2023-01-01T00:00:00Z)",
			})
			return
		}
	}

	limit := 0
	if limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 {
			limit = parsedLimit
		} else {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid 'limit' parameter. Must be a positive integer",
			})
			return
		}
	}

	if from.After(to) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid time range: 'from' must be before 'to'",
		})
		return
	}

	// Use mvrk token for /quotes endpoint
	quotesList, err := h.action.Execute(c.Request.Context(), from, to, limit, string(domainQuotes.TokenMVRK))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "Failed to get quotes",
			"details": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, quotesList)
}
