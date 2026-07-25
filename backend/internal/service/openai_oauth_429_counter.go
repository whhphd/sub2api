package service

import "context"

// OpenAIOAuth429CounterCache tracks consecutive 429 responses for an account.
// Implementations must make increments atomic across gateway instances.
type OpenAIOAuth429CounterCache interface {
	IncrementOpenAIOAuth429Count(ctx context.Context, accountID int64) (int64, error)
	ResetOpenAIOAuth429Count(ctx context.Context, accountID int64) error
}
