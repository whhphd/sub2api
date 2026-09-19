package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTurnStateHoldExhaustionReports503(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		err := &service.UpstreamFailoverError{StatusCode: 503, Reason: service.OpenAITurnStateHoldReason, ClientStatusCode: 503, ClientMessage: "waiting for baseline candidate for gpt-test"}
		h := &OpenAIGatewayHandler{}
		if bridge {
			h.handleAnthropicFailoverExhausted(c, err, false)
		} else {
			h.handleFailoverExhausted(c, err, false)
		}
		require.Equal(t, http.StatusServiceUnavailable, r.Code)
		require.Contains(t, r.Body.String(), "gpt-test")
	}
}
