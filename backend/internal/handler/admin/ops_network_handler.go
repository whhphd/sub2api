package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *OpsHandler) networkService(c *gin.Context) *service.OpsNetworkService {
	if h.opsService == nil || h.opsService.NetworkService() == nil {
		response.Error(c, http.StatusServiceUnavailable, "Network monitoring unavailable")
		return nil
	}
	if err := h.opsService.RequireMonitoringEnabled(c.Request.Context()); err != nil {
		response.ErrorFrom(c, err)
		return nil
	}
	return h.opsService.NetworkService()
}
func (h *OpsHandler) GetNetworkOverview(c *gin.Context) {
	s := h.networkService(c)
	if s == nil {
		return
	}
	result, err := s.GetOverview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpsHandler) GetNetworkSettings(c *gin.Context) {
	s := h.networkService(c)
	if s == nil {
		return
	}
	result, err := s.GetSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpsHandler) UpdateNetworkSettings(c *gin.Context) {
	s := h.networkService(c)
	if s == nil {
		return
	}
	var settings service.OpsNetworkSettings
	if err := c.ShouldBindJSON(&settings); err != nil {
		response.BadRequest(c, "Invalid network settings")
		return
	}
	result, err := s.UpdateSettings(c.Request.Context(), settings)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpsHandler) GetNetworkTrend(c *gin.Context) {
	s := h.networkService(c)
	if s == nil {
		return
	}
	start, end, err := parseOpsTimeRange(c, "1h")
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	link := c.DefaultQuery("link", "public")
	if (link != "public" && link != "private") || !start.Before(end) || end.Sub(start).Hours() > 180*24 {
		response.BadRequest(c, "Invalid network range or link")
		return
	}
	result, err := s.GetTrend(c.Request.Context(), link, start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
