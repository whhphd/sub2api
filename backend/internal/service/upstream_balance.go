package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	UpstreamBalanceExtraKey          = "upstream_balance"
	UpstreamBalanceAuthCredentialKey = "upstream_balance_auth"
	upstreamBalanceInterval          = 30 * time.Minute
	upstreamBalanceTimeout           = 20 * time.Second
)

var (
	ErrUpstreamBalanceUnavailable     = infraerrors.ServiceUnavailable("UPSTREAM_BALANCE_UNAVAILABLE", "upstream balance service unavailable")
	ErrUpstreamBalanceIdentityChanged = infraerrors.Conflict("UPSTREAM_BALANCE_IDENTITY_CHANGED", "account changed during balance query; retry")
	ErrUpstreamBalanceBusy            = infraerrors.TooManyRequests("UPSTREAM_BALANCE_BUSY", "balance query already running or recently completed")
)

// Balance is an observation, never an input to quota or scheduling decisions.
type UpstreamBalanceSnapshot struct {
	Status        string     `json:"status"`
	Provider      string     `json:"provider,omitempty"`
	Source        string     `json:"source,omitempty"`
	Scope         string     `json:"scope,omitempty"`
	Balance       *float64   `json:"balance,omitempty"`
	Currency      string     `json:"currency,omitempty"`
	ReceivedAt    *time.Time `json:"received_at,omitempty"`
	FreshUntil    *time.Time `json:"fresh_until,omitempty"`
	LastAttemptAt time.Time  `json:"last_attempt_at"`
	NextQueryAt   time.Time  `json:"next_query_at"`
	FailureCount  int        `json:"failure_count,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

type UpstreamBalanceAuthInput struct {
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id"`
	Clear       bool   `json:"clear"`
}

type upstreamBalanceAuth struct {
	AccessToken string `json:"access_token"`
	UserID      string `json:"user_id"`
	Identity    string `json:"identity"`
}

type upstreamBalanceRepository interface {
	ListDueUpstreamBalanceAccounts(context.Context, time.Time, int) ([]Account, error)
	UpdateUpstreamBalanceSnapshot(context.Context, *Account, *UpstreamBalanceSnapshot) error
}

type UpstreamBalanceService struct {
	probe         *UpstreamBillingProbeService
	encryptor     SecretEncryptor
	keyConfigured bool
	group         singleflight.Group
	slots         chan struct{}
	cycle         sync.Mutex
}

func NewUpstreamBalanceService(probe *UpstreamBillingProbeService, encryptor SecretEncryptor, keyConfigured bool) *UpstreamBalanceService {
	return &UpstreamBalanceService{probe: probe, encryptor: encryptor, keyConfigured: keyConfigured, slots: make(chan struct{}, 4)}
}

func (s *UpstreamBillingProbeService) BalanceService() *UpstreamBalanceService {
	if s == nil {
		return nil
	}
	return s.balance
}

func (s *UpstreamBalanceService) runLoop() {
	defer s.probe.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.probe.parentCtx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(s.probe.parentCtx); err != nil && s.probe.parentCtx.Err() == nil {
				slog.Warn("upstream_balance_cycle_failed", "error", "balance collection failed")
			}
		}
	}
}

func (s *UpstreamBalanceService) RunDue(ctx context.Context) error {
	s.cycle.Lock()
	defer s.cycle.Unlock()
	repo, ok := s.probe.accountRepo.(upstreamBalanceRepository)
	if !ok {
		return ErrUpstreamBalanceUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	release, acquired, err := s.probe.tryAcquireLeaderLock(ctx, "upstream:balance:leader")
	if err != nil || !acquired {
		return err
	}
	defer release()
	accounts, err := repo.ListDueUpstreamBalanceAccounts(ctx, s.probe.currentTime(), 12)
	if err != nil {
		return err
	}
	var group errgroup.Group
	group.SetLimit(4)
	for _, account := range accounts {
		id := account.ID
		group.Go(func() error {
			_, _ = s.query(ctx, id, true)
			return nil
		})
	}
	return group.Wait()
}

func (s *UpstreamBalanceService) Query(ctx context.Context, id int64) (*UpstreamBalanceSnapshot, error) {
	return s.query(ctx, id, false)
}

func (s *UpstreamBalanceService) query(ctx context.Context, id int64, scheduled bool) (*UpstreamBalanceSnapshot, error) {
	if s == nil || s.probe == nil || s.probe.accountRepo == nil {
		return nil, ErrUpstreamBalanceUnavailable
	}
	if id <= 0 {
		return nil, ErrUpstreamBillingProbeAccountInvalid
	}
	result := s.group.DoChan(strconv.FormatInt(id, 10), func() (any, error) {
		// Caller cancellation stops waiting, while the bounded shared query can finish for other callers.
		opCtx, cancel := context.WithTimeout(s.probe.parentCtx, upstreamBalanceTimeout)
		defer cancel()
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		case <-opCtx.Done():
			return nil, opCtx.Err()
		}
		release, acquired, err := s.probe.tryAcquireLeaderLock(opCtx, fmt.Sprintf("upstream:balance:account:%d", id))
		if err != nil {
			return nil, ErrUpstreamBalanceUnavailable
		}
		if !acquired {
			return nil, ErrUpstreamBalanceBusy
		}
		defer release()
		account, err := s.probe.accountRepo.GetByID(opCtx, id)
		if err != nil {
			return nil, err
		}
		if account.Type != AccountTypeAPIKey {
			return nil, ErrUpstreamBillingProbeAccountInvalid
		}
		previous := UpstreamBalanceFromAccount(account)
		now := s.probe.currentTime().UTC()
		if scheduled && (!account.IsActive() || (previous != nil && now.Before(previous.NextQueryAt))) {
			return previous, nil
		}
		if previous != nil && now.Sub(previous.LastAttemptAt) < 10*time.Second {
			return previous, nil
		}
		snapshot := s.fetch(opCtx, account)
		now = s.probe.currentTime().UTC()
		snapshot.LastAttemptAt = now
		delay := upstreamBalanceInterval
		switch snapshot.Status {
		case "ok", "unconfirmed":
			snapshot.ReceivedAt = probeTimePtr(now)
			snapshot.FreshUntil = probeTimePtr(now.Add(2 * upstreamBalanceInterval))
		case "failed", "unsupported":
			snapshot.FailureCount = 1
			if previous != nil {
				snapshot.FailureCount += previous.FailureCount
				snapshot.Balance, snapshot.Currency = previous.Balance, previous.Currency
				snapshot.Source, snapshot.Scope = previous.Source, previous.Scope
				if snapshot.Provider == "" {
					snapshot.Provider = previous.Provider
				}
				snapshot.ReceivedAt, snapshot.FreshUntil = previous.ReceivedAt, previous.FreshUntil
			}
			if snapshot.Status == "unsupported" {
				delay = 24 * time.Hour
			} else {
				delay *= time.Duration(1 << min(snapshot.FailureCount, 5))
			}
		}
		// Stable account jitter spreads recurring queries without changing the minimum cadence.
		snapshot.NextQueryAt = now.Add(delay + time.Duration(id%61)*time.Second)
		repo, ok := s.probe.accountRepo.(upstreamBalanceRepository)
		if !ok {
			return nil, ErrUpstreamBalanceUnavailable
		}
		if err := repo.UpdateUpstreamBalanceSnapshot(opCtx, account, snapshot); err != nil {
			return nil, err
		}
		return snapshot, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		if r.Err != nil {
			return nil, r.Err
		}
		v, _ := r.Val.(*UpstreamBalanceSnapshot)
		return v, nil
	}
}

func UpstreamBalanceFromAccount(account *Account) *UpstreamBalanceSnapshot {
	if account == nil || account.Type != AccountTypeAPIKey {
		return nil
	}
	raw, ok := account.Extra[UpstreamBalanceExtraKey]
	if !ok {
		return nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot UpstreamBalanceSnapshot
	if json.Unmarshal(body, &snapshot) != nil {
		return nil
	}
	switch snapshot.Status {
	case "ok", "unconfirmed", "non_wallet", "unsupported", "failed":
		return &snapshot
	default:
		return nil
	}
}

func upstreamBalanceCredentialIdentity(account *Account) string {
	body, _ := json.Marshal([]string{account.Platform, account.Type, account.GetCredential("base_url"), account.GetCredential("api_key")})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// PrepareAuth returns ciphertext for the existing account update transaction.
func (s *UpstreamBalanceService) PrepareAuth(account *Account, input *UpstreamBalanceAuthInput) (*string, error) {
	if input == nil {
		return nil, nil
	}
	if account == nil || account.Type != AccountTypeAPIKey {
		return nil, ErrUpstreamBillingProbeAccountInvalid
	}
	if input.Clear {
		value := ""
		return &value, nil
	}
	if s == nil || !s.keyConfigured || s.encryptor == nil {
		return nil, infraerrors.BadRequest("UPSTREAM_BALANCE_ENCRYPTION_REQUIRED", "a fixed TOTP_ENCRYPTION_KEY is required")
	}
	token := strings.TrimSpace(input.AccessToken)
	token = strings.TrimPrefix(token, "Bearer ")
	userID := strings.TrimSpace(input.UserID)
	id, err := strconv.ParseInt(userID, 10, 64)
	if err != nil || id <= 0 || token == "" || len(token) > 8192 || strings.ContainsAny(token, "\r\n\t ") {
		return nil, infraerrors.BadRequest("UPSTREAM_BALANCE_AUTH_INVALID", "valid user access token and user ID are required")
	}
	auth := upstreamBalanceAuth{AccessToken: token, UserID: userID, Identity: upstreamBalanceCredentialIdentity(account)}
	body, err := json.Marshal(auth)
	if err != nil {
		return nil, ErrUpstreamBalanceUnavailable
	}
	ciphertext, err := s.encryptor.Encrypt(string(body))
	if err != nil {
		return nil, ErrUpstreamBalanceUnavailable
	}
	return &ciphertext, nil
}
