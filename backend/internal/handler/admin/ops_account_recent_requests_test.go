package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountRecentRequestsValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpsHandler{opsService: &service.OpsService{}}
	router := gin.New()
	router.GET("/recent", h.GetAccountRecentRequests)
	for _, q := range []string{"", "0", "-1", "abc", "1,,2", strings.Repeat("1,", 100) + "1"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/recent?account_ids="+q, nil))
		require.Equal(t, 400, w.Code, q)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/recent?account_ids=2,1,2", nil))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"1":[]`)
	require.Contains(t, w.Body.String(), `"2":[]`)
}
