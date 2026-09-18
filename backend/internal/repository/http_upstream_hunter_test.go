package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyKeyForLogRemovesCredentials(t *testing.T) {
	require.Equal(t, "socks5://proxy.example:1080", proxyKeyForLog("socks5://username:password@proxy.example:1080/path?secret=yes"))
	require.Equal(t, "invalid", proxyKeyForLog("nonsense"))
	require.Equal(t, "direct", proxyKeyForLog(""))
}
func TestHunterFreshTransportDoesNotPopulateSharedCache(t *testing.T) {
	// Exercise actual HTTP bytes, close/reuse semantics and redirect control offline.
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Header().Set("Location", "/never")
		w.WriteHeader(302)
	}))
	defer server.Close()
	s := &httpUpstreamService{}
	req, err := http.NewRequestWithContext(service.WithHTTPUpstreamRedirectsDisabled(context.Background()), "GET", server.URL, nil)
	require.NoError(t, err)
	resp, err := s.doFreshUpstream(req, "", 1, service.HTTPUpstreamProfileDefault)
	require.NoError(t, err)
	_, _ = io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 302, resp.StatusCode)
	require.Equal(t, 1, count)
	require.Empty(t, s.clients)
}
