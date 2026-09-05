package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type balanceTestRepo struct {
	*upstreamBillingProbeAccountRepo
}

func (r *balanceTestRepo) ListDueUpstreamBalanceAccounts(_ context.Context, _ time.Time, limit int) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Account
	for _, a := range r.accounts {
		out = append(out, *a)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *balanceTestRepo) UpdateUpstreamBalanceSnapshot(_ context.Context, account *Account, snapshot *UpstreamBalanceSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := r.accounts[account.ID]
	if stored == nil || !reflect.DeepEqual(account.Credentials, stored.Credentials) {
		return ErrUpstreamBalanceIdentityChanged
	}
	stored.Extra[UpstreamBalanceExtraKey] = snapshot
	return nil
}

type balanceHTTPFunc func(*http.Request) (*http.Response, error)

func (f balanceHTTPFunc) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return f(r)
}
func (f balanceHTTPFunc) DoWithTLS(r *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f(r)
}
func balanceResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func newBalanceTestService(httpClient HTTPUpstream) (*UpstreamBalanceService, *balanceTestRepo) {
	repo := &balanceTestRepo{&upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{1: {
		ID: 1, Name: "relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "test-key", "base_url": "https://relay.example/v1"}, Extra: map[string]any{"quota_limit": 77.0},
	}}}}
	probe := newUpstreamBillingProbeTestService(repo, httpClient, &upstreamBillingProbeSettingRepo{})
	return NewUpstreamBalanceService(probe, nil, false), repo
}

func TestUpstreamBalanceSub2APIWalletModes(t *testing.T) {
	for _, tc := range []struct {
		name, body, status string
		balance            *float64
	}{
		{"zero", `{"mode":"unrestricted","isValid":true,"balance":0,"unit":"USD"}`, "ok", float64Ptr(0)},
		{"negative", `{"mode":"unrestricted","isValid":true,"balance":-2.5,"unit":"USD"}`, "ok", float64Ptr(-2.5)},
		{"key_quota", `{"mode":"quota_limited","isValid":true,"remaining":100}`, "non_wallet", nil},
		{"subscription", `{"mode":"unrestricted","isValid":true,"subscription":{},"remaining":100}`, "non_wallet", nil},
		{"large_wallet", `{"mode":"unrestricted","isValid":true,"balance":100000001,"unit":"USD"}`, "ok", float64Ptr(100000001)},
		{"missing_balance", `{"mode":"unrestricted","isValid":true,"remaining":100}`, "", nil},
		{"error", `{"isValid":false,"balance":0}`, "", nil},
		{"html", `<html>login</html>`, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSub2APIWallet([]byte(tc.body))
			if tc.status == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.status, got.Status)
			require.Equal(t, tc.balance, got.Balance)
		})
	}
}

func TestUpstreamBalanceNewAPIAndFailureMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		subStatus                int
		total, usage, meta, want string
		value                    *float64
	}{
		{"ambiguous", 200, `{"object":"billing_subscription","hard_limit_usd":100}`, `{"total_usage":2500}`, `{"success":true,"data":{"quota_display_type":"CNY"}}`, "unconfirmed", float64Ptr(75)},
		{"wallet", 200, `{"object":"billing_subscription","hard_limit_usd":100}`, `{"total_usage":10000}`, `{"success":true,"data":{"quota_display_type":"USD","display_token_stat_enabled":false}}`, "ok", float64Ptr(0)},
		{"unlimited", 200, `{"object":"billing_subscription","hard_limit_usd":100000000}`, `{}`, `{}`, "non_wallet", nil},
		{"unsupported", 404, `{}`, `{}`, `{}`, "unsupported", nil},
		{"auth", 401, `{}`, `{}`, `{}`, "failed", nil},
		{"forbidden", 403, `{}`, `{}`, `{}`, "failed", nil},
		{"rate_limit", 429, `{}`, `{}`, `{}`, "failed", nil},
		{"server_error", 500, `{}`, `{}`, `{}`, "failed", nil},
		{"redirect", 302, `{}`, `{}`, `{}`, "failed", nil},
		{"business_error", 200, `{"error":{"message":"test-secret"}}`, `{}`, `{}`, "failed", nil},
		{"missing_usage", 200, `{"object":"billing_subscription","hard_limit_usd":100}`, `{}`, `{}`, "failed", nil},
		{"oversized", 200, strings.Repeat("x", upstreamBillingProbeMaxBodyBytes+1), `{}`, `{}`, "failed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newBalanceTestService(balanceHTTPFunc(func(req *http.Request) (*http.Response, error) {
				require.True(t, HTTPUpstreamRedirectsDisabled(req.Context()))
				switch req.URL.Path {
				case "/v1/usage":
					return balanceResponse(404, `{}`), nil
				case "/v1/dashboard/billing/subscription":
					return balanceResponse(tc.subStatus, tc.total), nil
				case "/v1/dashboard/billing/usage":
					return balanceResponse(200, tc.usage), nil
				case "/api/status":
					require.Empty(t, req.Header.Get("Authorization"))
					return balanceResponse(200, tc.meta), nil
				default:
					t.Fatalf("unexpected path %s", req.URL.Path)
					return nil, nil
				}
			}))
			got, err := svc.Query(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Status)
			require.Equal(t, tc.value, got.Balance)
			require.NotContains(t, got.LastError, "test-secret")
			require.Equal(t, 77.0, repo.accounts[1].Extra["quota_limit"])
			require.True(t, repo.accounts[1].Schedulable)
		})
	}
}

func TestUpstreamBalanceFailureKeepsSuccessAndRetries(t *testing.T) {
	status := 200
	svc, repo := newBalanceTestService(balanceHTTPFunc(func(*http.Request) (*http.Response, error) {
		return balanceResponse(status, `{"mode":"unrestricted","isValid":true,"balance":25,"unit":"USD"}`), nil
	}))
	now := time.Now().UTC()
	svc.probe.now = func() time.Time { return now }
	first, err := svc.Query(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "ok", first.Status)
	now = now.Add(31 * time.Minute)
	status = 500
	second, err := svc.Query(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, first.Balance, second.Balance)
	require.Equal(t, first.ReceivedAt, second.ReceivedAt)
	require.Equal(t, 1, second.FailureCount)
	require.True(t, second.NextQueryAt.After(now.Add(59*time.Minute)))
	require.Equal(t, second, UpstreamBalanceFromAccount(repo.accounts[1]))
}

func TestUpstreamBalanceDefaultRunnerAndOAuthExclusion(t *testing.T) {
	var calls atomic.Int32
	svc, repo := newBalanceTestService(balanceHTTPFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return balanceResponse(200, `{"mode":"unrestricted","isValid":true,"balance":1,"unit":"USD"}`), nil
	}))
	repo.accounts[2] = &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive}
	require.NoError(t, svc.RunDue(context.Background()))
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, svc.RunDue(context.Background()))
	require.EqualValues(t, 1, calls.Load())
	_, err := svc.Query(context.Background(), 2)
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)
}

func TestUpstreamBalanceCancellationAndIdentityChange(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	svc, repo := newBalanceTestService(balanceHTTPFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-finish
		return balanceResponse(200, `{"mode":"unrestricted","isValid":true,"balance":1,"unit":"USD"}`), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.Query(ctx, 1); done <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	repo.mu.Lock()
	repo.accounts[1].Credentials["api_key"] = "changed"
	repo.mu.Unlock()
	close(finish)
	// Join the same in-flight operation and ensure its stale result cannot persist.
	r := <-svc.group.DoChan("1", func() (any, error) { return nil, ErrUpstreamBalanceIdentityChanged })
	require.ErrorIs(t, r.Err, ErrUpstreamBalanceIdentityChanged)
	require.Nil(t, UpstreamBalanceFromAccount(repo.accounts[1]))
}

type balanceTestEncryptor struct{}

func (balanceTestEncryptor) Encrypt(s string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}
func (balanceTestEncryptor) Decrypt(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}

func TestUpstreamBalanceUserCredentialsAndBinding(t *testing.T) {
	svc, repo := newBalanceTestService(balanceHTTPFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/api/user/self":
			require.Equal(t, "Bearer user-token", req.Header.Get("Authorization"))
			require.Equal(t, "42", req.Header.Get("New-Api-User"))
			return balanceResponse(200, `{"success":true,"data":{"quota":5000000}}`), nil
		case "/api/status":
			return balanceResponse(200, `{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"CNY","usd_exchange_rate":7}}`), nil
		default:
			t.Fatalf("unexpected endpoint %s", req.URL.Path)
			return nil, nil
		}
	}))
	svc.encryptor, svc.keyConfigured = balanceTestEncryptor{}, true
	input := &UpstreamBalanceAuthInput{AccessToken: "user-token", UserID: "42"}
	encrypted, err := svc.PrepareAuth(repo.accounts[1], input)
	require.NoError(t, err)
	require.NotContains(t, *encrypted, "user-token")
	repo.accounts[1].Credentials[UpstreamBalanceAuthCredentialKey] = *encrypted
	got, err := svc.Query(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "wallet", got.Scope)
	require.Equal(t, 70.0, *got.Balance)
	require.Equal(t, "CNY", got.Currency)
	repo.accounts[1].Credentials["api_key"] = "different"
	delete(repo.accounts[1].Extra, UpstreamBalanceExtraKey)
	got, err = svc.Query(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "credentials_changed", got.LastError)
	redacted := RedactAuditBody([]byte(`{"upstream_balance_auth":{"access_token":"user-token","user_id":"42"}}`), "application/json")
	require.NotContains(t, redacted, "user-token")
	var decoded any
	require.NoError(t, json.Unmarshal([]byte(redacted), &decoded))
}
