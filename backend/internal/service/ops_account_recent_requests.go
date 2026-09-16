package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const OpsRecentRequestsMaxAccounts = 100
const OpsRecentRequestsPerAccount = 10

// OpsAccountRecentRequest contains only the fields used by the account timeline.
// Success and error records remain separate events, not a user success-rate metric.
type OpsAccountRecentRequest struct {
	OpsRequestDetail
	ID                  int64    `json:"id"`
	UserEmail           string   `json:"user_email,omitempty"`
	AccountName         string   `json:"account_name,omitempty"`
	UpstreamModel       string   `json:"upstream_model,omitempty"`
	RequestType         string   `json:"request_type,omitempty"`
	OpenAIWSMode        bool     `json:"openai_ws_mode"`
	InputTokens         *int     `json:"input_tokens"`
	OutputTokens        *int     `json:"output_tokens"`
	CacheReadTokens     *int     `json:"cache_read_tokens"`
	CacheCreationTokens *int     `json:"cache_creation_tokens"`
	ImageInputTokens    *int     `json:"image_input_tokens"`
	ImageOutputTokens   *int     `json:"image_output_tokens"`
	ActualCost          *float64 `json:"actual_cost"`
	AccountCost         *float64 `json:"account_cost"`
}

type OpsAccountRecentRequests struct {
	Accounts    map[int64][]*OpsAccountRecentRequest `json:"accounts"`
	SampledAt   time.Time                            `json:"sampled_at"`
	WindowHours int                                  `json:"window_hours"`
	Limit       int                                  `json:"limit"`
}

func NormalizeOpsRecentAccountIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 || len(ids) > OpsRecentRequestsMaxAccounts {
		return nil, fmt.Errorf("account_ids must contain 1-%d IDs", OpsRecentRequestsMaxAccounts)
	}
	seen := make(map[int64]bool, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("invalid account_id")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *OpsService) GetAccountRecentRequests(ctx context.Context, ids []int64) (*OpsAccountRecentRequests, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	ids, err := NormalizeOpsRecentAccountIDs(ids)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := &OpsAccountRecentRequests{Accounts: make(map[int64][]*OpsAccountRecentRequest, len(ids)), SampledAt: now, WindowHours: 24, Limit: OpsRecentRequestsPerAccount}
	for _, id := range ids {
		out.Accounts[id] = []*OpsAccountRecentRequest{}
	}
	if s.opsRepo == nil {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	items, err := s.opsRepo.GetAccountRecentRequests(ctx, ids, now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item == nil || item.AccountID == nil {
			continue
		}
		if _, ok := out.Accounts[*item.AccountID]; !ok {
			continue
		}
		item.Message = logredact.RedactText(redactContentModerationSecrets(item.Message), "api_key", "authorization", "cookie")
		out.Accounts[*item.AccountID] = append(out.Accounts[*item.AccountID], item)
	}
	return out, nil
}
