package service

// Protocol boundaries follow Wei-Shaw/sub2api PR #4672 (yardbirds0),
// adapted to persisted wallet observations and optional New API user auth.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

type upstreamBalanceClient struct {
	account  *Account
	upstream HTTPUpstream
	base     string
	proxy    string
	tls      *tlsfingerprint.Profile
}

func (s *UpstreamBalanceService) fetch(ctx context.Context, account *Account) *UpstreamBalanceSnapshot {
	fail := func(reason string) *UpstreamBalanceSnapshot {
		return &UpstreamBalanceSnapshot{Status: "failed", LastError: reason}
	}
	if upstreamBillingProbeTargetIsOfficialAPI(account.GetCredential("base_url")) {
		return &UpstreamBalanceSnapshot{Status: "unsupported"}
	}
	if s.probe.accountTestService == nil || s.probe.accountTestService.httpUpstream == nil {
		return fail("transport_unavailable")
	}
	base, err := s.probe.accountTestService.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return fail("invalid_base_url")
	}
	queryCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()
	client := &upstreamBalanceClient{account: account, base: base, upstream: s.probe.accountTestService.httpUpstream}
	if account.ProxyID != nil {
		if account.Proxy == nil || account.Proxy.ID != *account.ProxyID {
			return fail("proxy_unavailable")
		}
		client.proxy = account.Proxy.URL()
	}
	if s.probe.accountTestService.tlsFPProfileService != nil {
		client.tls = s.probe.accountTestService.tlsFPProfileService.ResolveTLSProfile(account)
	}
	if ciphertext := account.GetCredential(UpstreamBalanceAuthCredentialKey); ciphertext != "" {
		if s.encryptor == nil || !s.keyConfigured {
			return fail("credentials_unavailable")
		}
		plain, err := s.encryptor.Decrypt(ciphertext)
		if err != nil {
			return fail("credentials_unavailable")
		}
		var auth upstreamBalanceAuth
		if json.Unmarshal([]byte(plain), &auth) != nil || auth.Identity != upstreamBalanceCredentialIdentity(account) {
			return fail("credentials_changed")
		}
		result, err := client.queryNewAPIUser(queryCtx, auth)
		if err != nil {
			return &UpstreamBalanceSnapshot{Status: "failed", Provider: "new_api", LastError: balanceErrorCode(err)}
		}
		return result
	}
	if account.GetCredential("api_key") == "" {
		return fail("missing_api_key")
	}
	body, status, err := client.get(queryCtx, buildOpenAIEndpointURL(base, "/v1/usage"), account.GetCredential("api_key"), "")
	if err != nil {
		return fail(balanceErrorCode(err))
	}
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed {
		result, err := client.queryNewAPIBilling(queryCtx)
		if err != nil {
			return &UpstreamBalanceSnapshot{Status: "failed", Provider: "new_api", LastError: balanceErrorCode(err)}
		}
		return result
	}
	if err := balanceHTTPError(status); err != nil {
		return fail(balanceErrorCode(err))
	}
	result, err := parseSub2APIWallet(body)
	if err != nil {
		return &UpstreamBalanceSnapshot{Status: "failed", Provider: "sub2api", LastError: "invalid_response"}
	}
	return result
}

func (c *upstreamBalanceClient) get(ctx context.Context, endpoint, token, userID string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, errors.New("invalid_base_url")
	}
	profile := HTTPUpstreamProfileDefault
	if c.account.Platform == PlatformOpenAI {
		profile = HTTPUpstreamProfileOpenAI
	}
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(ctx, profile)))
	c.account.ApplyHeaderOverrides(req.Header)
	// Public metadata carries no credentials; user auth must not inherit the model key.
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "X-Api-Key", "Api-Key", "New-Api-User", "X-Goog-Api-Key"} {
		req.Header.Del(key)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if userID != "" {
		req.Header.Set("New-Api-User", userID)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.upstream.DoWithTLS(req, c.proxy, c.account.ID, max(c.account.Concurrency, 1), c.tls)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, errors.New("timeout")
		}
		return nil, 0, errors.New("request_failed")
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, errors.New("invalid_response")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, upstreamBillingProbeMaxBodyBytes+1))
	if err != nil || len(body) > upstreamBillingProbeMaxBodyBytes {
		return nil, resp.StatusCode, errors.New("invalid_response")
	}
	return body, resp.StatusCode, nil
}

func balanceHTTPError(status int) error {
	switch {
	case status == 401 || status == 403:
		return errors.New("authentication_failed")
	case status == 429:
		return errors.New("rate_limited")
	case status >= 300 && status < 400:
		return errors.New("redirect_rejected")
	case status < 200 || status >= 300:
		return errors.New("http_error")
	default:
		return nil
	}
}

func balanceErrorCode(err error) string {
	switch err.Error() {
	case "invalid_base_url", "timeout", "request_failed", "invalid_response", "authentication_failed", "rate_limited", "redirect_rejected", "http_error":
		return err.Error()
	default:
		return "request_failed"
	}
}

func finiteBalance(v *float64) bool { return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) }

func parseSub2APIWallet(body []byte) (*UpstreamBalanceSnapshot, error) {
	var response struct {
		Mode         string          `json:"mode"`
		IsValid      *bool           `json:"isValid"`
		Balance      *float64        `json:"balance"`
		Remaining    *float64        `json:"remaining"`
		Unit         string          `json:"unit"`
		Subscription json.RawMessage `json:"subscription"`
		Quota        json.RawMessage `json:"quota"`
		RateLimits   json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(body, &response) != nil || response.IsValid == nil || !*response.IsValid {
		return nil, errors.New("invalid_response")
	}
	result := &UpstreamBalanceSnapshot{Status: "non_wallet", Provider: "sub2api", Source: "/v1/usage"}
	if response.Mode == "quota_limited" || hasBalanceJSON(response.Subscription) || hasBalanceJSON(response.Quota) || hasBalanceJSON(response.RateLimits) {
		return result, nil
	}
	if response.Mode != "unrestricted" || !finiteBalance(response.Balance) {
		return nil, errors.New("invalid_response")
	}
	result.Status, result.Scope, result.Balance = "ok", "wallet", response.Balance
	result.Currency = balanceCurrency(response.Unit)
	return result, nil
}

func hasBalanceJSON(value json.RawMessage) bool { return len(value) > 0 && string(value) != "null" }

type newAPIBalanceMetadata struct {
	QuotaPerUnit     float64 `json:"quota_per_unit"`
	DisplayType      string  `json:"quota_display_type"`
	ExchangeRate     float64 `json:"usd_exchange_rate"`
	DisplayTokenStat *bool   `json:"display_token_stat_enabled"`
}

func (c *upstreamBalanceClient) rootEndpoint(path string) string {
	u, _ := url.Parse(c.base)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1") + path
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	return u.String()
}

func (c *upstreamBalanceClient) metadata(ctx context.Context) newAPIBalanceMetadata {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, status, err := c.get(ctx, c.rootEndpoint("/api/status"), "", "")
	var response struct {
		Success bool                  `json:"success"`
		Data    newAPIBalanceMetadata `json:"data"`
	}
	if err == nil && status == 200 && json.Unmarshal(body, &response) == nil && response.Success {
		return response.Data
	}
	return newAPIBalanceMetadata{}
}

func (c *upstreamBalanceClient) queryNewAPIBilling(ctx context.Context) (*UpstreamBalanceSnapshot, error) {
	key := c.account.GetCredential("api_key")
	body, status, err := c.get(ctx, buildOpenAIEndpointURL(c.base, "/v1/dashboard/billing/subscription"), key, "")
	if err != nil {
		return nil, err
	}
	if status == 404 || status == 405 {
		return &UpstreamBalanceSnapshot{Status: "unsupported"}, nil
	}
	if err := balanceHTTPError(status); err != nil {
		return nil, err
	}
	var sub struct {
		Object string          `json:"object"`
		Total  *float64        `json:"hard_limit_usd"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &sub) != nil || hasBalanceJSON(sub.Error) || sub.Object != "billing_subscription" || !finiteBalance(sub.Total) || *sub.Total < 0 {
		return nil, errors.New("invalid_response")
	}
	result := &UpstreamBalanceSnapshot{Status: "unconfirmed", Provider: "new_api", Source: "/v1/dashboard/billing", Scope: "unknown"}
	// New API returns this sentinel for unlimited keys, not a funded wallet.
	if *sub.Total == 100000000 {
		result.Status = "non_wallet"
		return result, nil
	}
	body, status, err = c.get(ctx, buildOpenAIEndpointURL(c.base, "/v1/dashboard/billing/usage"), key, "")
	if err != nil {
		return nil, err
	}
	if err := balanceHTTPError(status); err != nil {
		return nil, err
	}
	var usage struct {
		Used  *float64        `json:"total_usage"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &usage) != nil || hasBalanceJSON(usage.Error) || !finiteBalance(usage.Used) || *usage.Used < 0 {
		return nil, errors.New("invalid_response")
	}
	remaining := *sub.Total - *usage.Used/100
	if !finiteBalance(&remaining) {
		return nil, errors.New("invalid_response")
	}
	meta := c.metadata(ctx)
	result.Balance, result.Currency = &remaining, balanceCurrency(meta.DisplayType)
	if meta.DisplayTokenStat != nil && !*meta.DisplayTokenStat {
		result.Status, result.Scope = "ok", "wallet"
	}
	return result, nil
}

func (c *upstreamBalanceClient) queryNewAPIUser(ctx context.Context, auth upstreamBalanceAuth) (*UpstreamBalanceSnapshot, error) {
	body, status, err := c.get(ctx, c.rootEndpoint("/api/user/self"), auth.AccessToken, auth.UserID)
	if err != nil {
		return nil, err
	}
	if err := balanceHTTPError(status); err != nil {
		return nil, err
	}
	var response struct {
		Success bool `json:"success"`
		Data    *struct {
			Quota *float64 `json:"quota"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || !response.Success || response.Data == nil || !finiteBalance(response.Data.Quota) {
		return nil, errors.New("invalid_response")
	}
	meta := c.metadata(ctx)
	// Without the site's conversion metadata, retain raw quota rather than invent a currency.
	value, currency := *response.Data.Quota, "TOKENS"
	if meta.QuotaPerUnit > 0 && !math.IsInf(meta.QuotaPerUnit, 0) && meta.DisplayType != "TOKENS" {
		value /= meta.QuotaPerUnit
		currency = "USD"
		if meta.DisplayType == "CNY" && meta.ExchangeRate > 0 {
			value *= meta.ExchangeRate
			currency = "CNY"
		}
	}
	if !finiteBalance(&value) {
		return nil, errors.New("invalid_response")
	}
	return &UpstreamBalanceSnapshot{Status: "ok", Provider: "new_api", Source: "/api/user/self", Scope: "wallet", Balance: &value, Currency: currency}, nil
}

func balanceCurrency(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "USD":
		return "USD"
	case "CNY":
		return "CNY"
	case "TOKENS":
		return "TOKENS"
	default:
		return ""
	}
}
