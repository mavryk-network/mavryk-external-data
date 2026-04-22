package get_by_token

import (
	"quotes/internal/core/api/http/common"
	"quotes/internal/core/application/quotes/get_by_token"
	coreerrors "quotes/internal/core/common/errors"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	action *get_by_token.Action
}

func New(action *get_by_token.Action) *Handler {
	return &Handler{action: action}
}

// GetQuotesByToken godoc
// @Summary      Get quotes for a specific token
// @Description  Retrieve quotes for a specific token (mvrk, usdt, etc.) with optional filters. If no time range is specified, returns the latest 100 quotes by default.
// @Tags         tokens
// @Accept       json
// @Produce      json
// @Param        token   path      string  true   "Token name (e.g., mvrk, usdt)"
// @Param        from    query     string  false  "Start time (RFC3339 format, e.g., 2025-01-01T00:00:00Z). If not specified, returns latest quotes"
// @Param        to      query     string  false  "End time (RFC3339 format, e.g., 2025-01-01T23:59:59Z). If not specified, returns latest quotes"
// @Param        limit   query     int     false  "Maximum number of quotes to return. Default: 100 when no time range specified, no limit when time range is specified"
// @Success      200     {array}   quotes.Quote  "List of quotes"
// @Failure      400     {object}  map[string]string  "Invalid request parameters"
// @Failure      404     {object}  map[string]string  "Token not found"
// @Failure      500     {object}  map[string]string  "Internal server error"
// @Router       /{token} [get]
func (h *Handler) Handle(c *gin.Context) {
	tokenName := c.Param("token")
	if tokenName == "" {
		common.RespondError(c, coreerrors.InvalidArgument("Token name is required"))
		return
	}

	q, err := common.BindQuotesQuery(c, common.QuotesQueryOptions{
		Mode:               common.QuotesQueryModeByToken,
		DefaultLatestLimit: 100,
	})
	if err != nil {
		common.RespondError(c, err)
		return
	}

	quotes, err := h.action.Execute(c.Request.Context(), tokenName, q.From, q.To, q.Limit)
	if err != nil {
		if get_by_token.IsTokenNotFound(err) {
			common.RespondError(c, coreerrors.NotFound("Token not found"))
			return
		}
		common.RespondError(c, coreerrors.Internal("Unable to load quotes", err))
		return
	}

	c.JSON(200, quotes)
}
