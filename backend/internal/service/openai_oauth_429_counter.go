package service

import (
	"context"
	"fmt"
)

// OpenAIOAuth429Observation describes one eligible upstream outcome. A window
// is dormant until its first 429; non-429 outcomes only count while active.
type OpenAIOAuth429Observation struct {
	AccountID                 int64
	PolicyRevision            int64
	WindowSeconds             int
	MinimumSamples            int
	Minimum429Count           int
	RatioThresholdBasisPoints int64
	Is429                     bool
	ResetAtUnix               int64
}

func (o OpenAIOAuth429Observation) Validate() error {
	if o.AccountID <= 0 {
		return fmt.Errorf("account ID must be positive")
	}
	if o.PolicyRevision < 1 {
		return fmt.Errorf("policy revision must be positive")
	}
	if o.WindowSeconds < minOpenAIOAuth429WindowSeconds || o.WindowSeconds > maxOpenAIOAuth429WindowSeconds {
		return fmt.Errorf("window seconds must be between %d and %d", minOpenAIOAuth429WindowSeconds, maxOpenAIOAuth429WindowSeconds)
	}
	if o.MinimumSamples < minOpenAIOAuth429MinimumSamples || o.MinimumSamples > maxOpenAIOAuth429MinimumSamples {
		return fmt.Errorf("minimum samples must be between %d and %d", minOpenAIOAuth429MinimumSamples, maxOpenAIOAuth429MinimumSamples)
	}
	if o.Minimum429Count < 1 || o.Minimum429Count > o.MinimumSamples {
		return fmt.Errorf("minimum 429 count must be between 1 and minimum samples")
	}
	if o.RatioThresholdBasisPoints < 100 || o.RatioThresholdBasisPoints > openAIOAuth429RatioScale {
		return fmt.Errorf("ratio threshold basis points must be between 100 and %d", openAIOAuth429RatioScale)
	}
	return nil
}

// OpenAIOAuth429ObservationResult is the atomic state after an observation.
// TriggerClaimed is true for exactly one observer per window across instances.
type OpenAIOAuth429ObservationResult struct {
	Active            bool
	WindowStartUnix   int64
	TotalSamples      int64
	Count429          int64
	LatestResetAtUnix int64
	Triggered         bool
	TriggerClaimed    bool
}

// OpenAIOAuth429CounterCache is the legacy injection-linked streak contract.
// It remains during the staged rollout so existing wiring stays compatible;
// the completed runtime policy uses OpenAIOAuth429ObservationCache exclusively.
type OpenAIOAuth429CounterCache interface {
	IncrementOpenAIOAuth429Count(ctx context.Context, accountID int64) (int64, error)
	ResetOpenAIOAuth429Count(ctx context.Context, accountID int64) error
}

// OpenAIOAuth429ObservationCache stores the account-local fixed window.
// Observe must be atomic across gateway instances.
type OpenAIOAuth429ObservationCache interface {
	ObserveOpenAIOAuth429(ctx context.Context, observation OpenAIOAuth429Observation) (OpenAIOAuth429ObservationResult, error)
}
