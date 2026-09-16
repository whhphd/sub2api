package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GET /api/v1/admin/ops/accounts/recent-requests?account_ids=1,2
func (h *OpsHandler) GetAccountRecentRequests(c *gin.Context) {
	if h.opsService == nil {
		response.Error(c, http.StatusServiceUnavailable, "Ops service not available")
		return
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	parts := strings.Split(c.Query("account_ids"), ",")
	if len(parts) > service.OpsRecentRequestsMaxAccounts {
		response.BadRequest(c, "Too many account IDs")
		return
	}
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid account_ids")
			return
		}
		ids = append(ids, id)
	}
	out, err := h.opsService.GetAccountRecentRequests(c.Request.Context(), ids)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, "Failed to load recent account requests")
		return
	}
	response.Success(c, out)
}
