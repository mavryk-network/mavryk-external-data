package get_all

import (
	"quotes/internal/core/api/http/common"
	"quotes/internal/core/application/quotes/get_all"
	coreerrors "quotes/internal/core/common/errors"
	domainQuotes "quotes/internal/core/domain/quotes"

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
	q, err := common.BindQuotesQuery(c, common.QuotesQueryOptions{Mode: common.QuotesQueryModeGetAll})
	if err != nil {
		common.RespondError(c, err)
		return
	}

	quotesList, err := h.action.Execute(c.Request.Context(), q.From, q.To, q.Limit, string(domainQuotes.TokenMVRK))
	if err != nil {
		common.RespondError(c, coreerrors.Internal("Unable to load quotes", err))
		return
	}

	c.JSON(200, quotesList)
}
