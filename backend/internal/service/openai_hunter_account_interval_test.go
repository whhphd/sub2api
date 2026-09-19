package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHunterAccountWorkersIndependentAndSequential(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	cfg.GapSeconds = 5
	_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(context.Background(), &cfg, nil, nil)
	require.NoError(t, err)
	b := *cloneStateAccount(a)
	b.ID++
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slowStarted := make(chan struct{})
	fastStarted := make(chan int, 4)
	resume := make(chan struct{})
	intervals := make(chan time.Duration, 4)
	var fastCount, slowCount atomic.Int32
	s.attemptOverride = func(ctx context.Context, account *Account, _ TurnStateHunterSettings) (bool, bool) {
		if account.ID == a.ID {
			if slowCount.Add(1) == 1 { close(slowStarted) }
			<-ctx.Done()
			return true, true
		}
		fastStarted <- int(fastCount.Add(1))
		return true, false
	}
	s.accountWait = func(ctx context.Context, gap time.Duration) error {
		intervals <- gap
		select {
		case <-ctx.Done(): return ctx.Err()
		case <-resume: return nil
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); s.runAccountWorkers(ctx, []Account{*a, b, *a}, cfg) }()
	select { case <-slowStarted: case <-time.After(3*time.Second): t.Fatal("slow worker did not start") }
	select { case n := <-fastStarted: require.Equal(t, 1, n); case <-time.After(3*time.Second): t.Fatal("slow account blocked other accounts") }
	require.Equal(t, 5*time.Second, <-intervals)
	require.EqualValues(t, 1, fastCount.Load(), "account cannot run again before its own interval")
	resume <- struct{}{}
	select { case n := <-fastStarted: require.Equal(t, 2, n); case <-time.After(3*time.Second): t.Fatal("fast account did not resume independently") }
	cancel()
	select { case <-done: case <-time.After(3*time.Second): t.Fatal("cancellation did not join all account workers") }
	require.EqualValues(t, 1, slowCount.Load(), "duplicate IDs must not create overlapping probes")
}

func TestHunterAccountWorkerRechecksPolicyAfterInterval(t *testing.T) {
	s, a, cfg := newHunterTest(t)
	var calls int
	s.attemptOverride = func(context.Context, *Account, TurnStateHunterSettings) (bool, bool) { calls++; return false, false }
	s.accountWait = func(context.Context, time.Duration) error {
		cfg.Enabled = false
		_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(context.Background(), &cfg, nil, nil)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.runAccountWorkers(ctx, []Account{*a}, cfg)
	require.NoError(t, ctx.Err())
	require.Equal(t, 1, calls)
}

func TestHunterAccountIntervalCancellation(t *testing.T) {
	s, _, _ := newHunterTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, s.waitAccountInterval(ctx, time.Hour), context.Canceled)
}

func TestHunterExemptModelsNeverActivelyProbed(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false:"configured", true:"auto_discovered"}[auto], func(t *testing.T) {
			s, a, cfg := newHunterTest(t)
			cfg.AutoModels = auto
			cfg.Models = []string{"gpt-5.6-terra", "gpt-test"}
			cfg.HoldExcludedModels = []string{"gpt-5.6-terra"}
			s.gateway.turnStateTraffic.note(a.ID, "gpt-5.6-terra", s.now())
			s.gateway.turnStateTraffic.note(a.ID, "gpt-test", s.now())
			_, err := s.gateway.settingService.UpdateOpenAIOAuthRuntimePolicy(context.Background(), &cfg, nil, nil)
			require.NoError(t, err)
			var probed []string
			s.probeOverride = func(_ context.Context, _ *Account, model string, _ TurnStateHunterSettings, proxy Proxy) openAITurnStateHuntAttempt {
				probed = append(probed, model)
				return openAITurnStateHuntAttempt{At:s.now(), Model:model, ProxyID:proxy.ID, Status:429, Error:"rate_limited"}
			}
			spent, halt := s.huntOne(context.Background(), a, cfg)
			require.True(t, spent)
			require.False(t, halt)
			require.Equal(t, []string{"gpt-test"}, probed)
			// The only non-exempt model now has a short-429 cooldown. Do not fall back to Terra.
			spent, halt = s.huntOne(context.Background(), a, cfg)
			require.False(t, spent)
			require.False(t, halt)
			require.Len(t, probed, 1)
		})
	}
}
