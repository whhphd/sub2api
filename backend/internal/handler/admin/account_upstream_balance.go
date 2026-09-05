package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) QueryUpstreamBalance(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid account ID")
		return
	}
	if h.upstreamBillingProbe == nil {
		response.ErrorFrom(c, service.ErrUpstreamBalanceUnavailable)
		return
	}
	balance := h.upstreamBillingProbe.BalanceService()
	if balance == nil {
		response.ErrorFrom(c, service.ErrUpstreamBalanceUnavailable)
		return
	}
	snapshot, err := balance.Query(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, snapshot)
}
