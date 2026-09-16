//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountRecentRequestsIndexedBatch(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	var userID, keyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'test') RETURNING id`, fmt.Sprintf("recent-%d@example.test", now.UnixNano())).Scan(&userID))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM users WHERE id=$1`, userID) })
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys (user_id,key,name) VALUES ($1,$2,'recent') RETURNING id`, userID, fmt.Sprintf("recent-key-%d", now.UnixNano())).Scan(&keyID))
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM api_keys WHERE id=$1`, keyID) })
	ids := make([]int64, 3)
	for i := range ids {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts (name,platform,type,credentials) VALUES ('recent','openai','oauth','{}') RETURNING id`).Scan(&ids[i]))
		id := ids[i]
		t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM accounts WHERE id=$1`, id) })
	}
	t.Cleanup(func() {
		_, _ = integrationDB.Exec(`DELETE FROM usage_logs WHERE user_id=$1`, userID)
		_, _ = integrationDB.Exec(`DELETE FROM ops_error_logs WHERE user_id=$1`, userID)
	})
	for i := 0; i < 14; i++ {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO usage_logs (user_id,api_key_id,account_id,request_id,model,created_at,input_tokens,output_tokens,actual_cost,total_cost,account_stats_cost,account_rate_multiplier,request_type,stream,first_token_ms)
   VALUES ($1,$2,$3,$4,'model',$5,0,12,0,99,2,0.5,3,true,0)`, userID, keyID, ids[0], fmt.Sprintf("recent-%d", i), now.Add(-time.Duration(i+1)*time.Minute))
		require.NoError(t, err)
	}
	for _, v := range []struct {
		id   int64
		at   time.Time
		code int
		req  string
	}{{ids[0], now.Add(-30 * time.Second), 502, "retry-error"}, {ids[1], now.Add(-time.Minute), 429, "limited"}, {ids[1], now.Add(-25 * time.Hour), 502, "too-old"}, {ids[1], now.Add(-time.Second), 200, "not-error"}, {ids[2], now.Add(-time.Second), 502, "not-requested"}} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO ops_error_logs (user_id,account_id,request_id,created_at,status_code,error_phase,error_type,request_type,error_message) VALUES ($1,$2,$3,$4,$5,'upstream','test',3,'failed')`, userID, v.id, v.req, v.at, v.code)
		require.NoError(t, err)
	}
	repo := &opsRepository{db: integrationDB}
	items, err := repo.GetAccountRecentRequests(ctx, []int64{ids[0], ids[1]}, now.Add(-24*time.Hour), now)
	require.NoError(t, err)
	require.Len(t, items, 11)
	require.Equal(t, "retry-error", items[0].RequestID)
	require.Nil(t, items[0].ActualCost)
	require.Equal(t, "ws_v2", items[0].RequestType)
	require.Equal(t, "recent-0", items[1].RequestID)
	require.Equal(t, 0, *items[1].InputTokens)
	require.Equal(t, 0, *items[1].FirstTokenMs)
	require.Equal(t, 0.0, *items[1].ActualCost)
	require.Equal(t, 1.0, *items[1].AccountCost)
	require.Equal(t, "ws_v2", items[1].RequestType)
	require.NotEmpty(t, items[1].UserEmail)
	require.Equal(t, "limited", items[10].RequestID)
	for _, item := range items {
		require.NotEqual(t, ids[2], *item.AccountID)
	}
}
