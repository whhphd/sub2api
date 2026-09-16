package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type recentRequestsRepoMock struct {
	opsRepoMock
	calls      int
	ids        []int64
	start, end time.Time
}

func (m *recentRequestsRepoMock) GetAccountRecentRequests(ctx context.Context, ids []int64, start, end time.Time) ([]*OpsAccountRecentRequest, error) {
	m.calls++
	m.ids = ids
	m.start, m.end = start, end
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		panic("missing bounded query")
	}
	id := ids[0]
	return []*OpsAccountRecentRequest{{ID: 1, OpsRequestDetail: OpsRequestDetail{AccountID: &id, Message: "Authorization: Bearer secret-access-token-value"}}}, nil
}
func TestAccountRecentRequestsScopeAndBounds(t *testing.T) {
	repo := &recentRequestsRepoMock{}
	s := &OpsService{opsRepo: repo}
	out, err := s.GetAccountRecentRequests(context.Background(), []int64{2, 1, 2})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, repo.ids)
	require.Equal(t, 24*time.Hour, repo.end.Sub(repo.start))
	require.Len(t, out.Accounts[1], 1)
	require.NotContains(t, out.Accounts[1][0].Message, "secret-access-token-value")
	require.NotNil(t, out.Accounts[2])
	require.Empty(t, out.Accounts[2])
	require.Equal(t, 10, out.Limit)
	for _, ids := range [][]int64{nil, {0}, {-1}, make([]int64, 101)} {
		_, err := s.GetAccountRecentRequests(context.Background(), ids)
		require.Error(t, err)
	}
	require.Equal(t, 1, repo.calls)
	s.cfg = &config.Config{}
	_, err = s.GetAccountRecentRequests(context.Background(), []int64{1})
	require.ErrorIs(t, err, ErrOpsDisabled)
	require.Equal(t, 1, repo.calls)
}
