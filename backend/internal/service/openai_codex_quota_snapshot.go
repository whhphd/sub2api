package service

import (
	"context"
	"fmt"
)

type codexQuotaSnapshotContextKey struct{}

// Reuse the immutable account snapshot from the KlN transplant without replacing
// CallAI's usage parsing, credit reset, pause, or compensation logic.
func (s *OpenAIQuotaService) codexQuotaSnapshotContext(ctx context.Context, id int64) (context.Context, error) {
	if s == nil || s.accountRepo == nil {
		return ctx, nil
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return ctx, err
	}
	if account == nil {
		return ctx, fmt.Errorf("account not found")
	}
	rows := map[int64]*Account{id: snapshotOpenAIOutboundAccount(account)}
	if account.IsShadow() {
		parent, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return ctx, err
		}
		if parent != nil {
			rows[parent.ID] = snapshotOpenAIOutboundAccount(parent)
		}
	}
	return context.WithValue(ctx, codexQuotaSnapshotContextKey{}, rows), nil
}
func (s *OpenAIQuotaService) codexQuotaSnapshotAccount(ctx context.Context, id int64) (*Account, error) {
	if rows, ok := ctx.Value(codexQuotaSnapshotContextKey{}).(map[int64]*Account); ok {
		if account := rows[id]; account != nil {
			return account, nil
		}
	}
	return s.accountRepo.GetByID(ctx, id)
}
func (s *OpenAIQuotaService) codexQuotaCredentialAccount(ctx context.Context, account *Account) (*Account, error) {
	if account != nil && account.ParentAccountID != nil {
		if rows, ok := ctx.Value(codexQuotaSnapshotContextKey{}).(map[int64]*Account); ok {
			if parent := rows[*account.ParentAccountID]; parent != nil {
				return parent, nil
			}
		}
	}
	return resolveCredentialAccount(ctx, s.accountRepo, account)
}
