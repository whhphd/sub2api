//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type codexPolicyChangingProxyRepo struct {
	ProxyRepository
	onList func()
}

func (r *codexPolicyChangingProxyRepo) ListActive(context.Context) ([]Proxy, error) {
	r.onList()
	return []Proxy{{ID: 2, Status: StatusActive, Protocol: "http", Host: "proxy.invalid", Port: 8080}}, nil
}

func TestCodexEnhancementEnabledDuringProxySelectionPreventsRotation(t *testing.T) {
	for _, healthyPool := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_pool", true: "healthy_pool"}[healthyPool], func(t *testing.T) {
			ctx := context.Background()
			settings := &SettingService{settingRepo: newOpenAIOAuthRuntimeSettingRepo()}
			yes := true
			_, err := settings.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, &yes)
			require.NoError(t, err)
			proxyRepo := &codexPolicyChangingProxyRepo{onList: func() {
				_, err := settings.UpdateOpenAIOAuthRuntimeSettings(ctx, nil, nil, nil, nil, nil, nil, &yes)
				require.NoError(t, err)
			}}
			repo := &openAI429SnapshotRepo{}
			svc := NewRateLimitService(repo, nil, nil, nil, nil)
			svc.SetSettingService(settings)
			svc.SetProxyRepository(proxyRepo)
			if healthyPool {
				svc.SetProxyHealthService(&ProxyHealthService{proxyRepo: proxyRepo})
			}
			id := int64(1)
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyID: &id}
			svc.rotateOpenAIOAuthProxyOnShort429(ctx, account, []byte(`{"error":{"message":"Rate limit exceeded"}}`))
			require.Nil(t, repo.bulkUpdatedPayload.ProxyID)
			require.Equal(t, int64(1), *account.ProxyID)
		})
	}
}
