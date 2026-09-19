package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.CodexTurnStateStore = (*accountRepository)(nil)

func (r *accountRepository) ReadCodexTurnState(ctx context.Context, id int64) (*service.Account, error) {
	return r.GetByID(ctx, id)
}

// Pool read-modify-write is protected across processes by the account row lock.
// Do not enqueue a scheduler rebuild or copy a partially hydrated account into its
// cache. Normal account edits acquire the same row lock and retain these keys.
func (r *accountRepository) MutateCodexTurnState(ctx context.Context, id int64, mutate func(*service.Account) (map[string]any, error)) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Client().QueryContext(ctx, "SELECT platform,type,credentials,extra FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE", id)
	if err != nil {
		return err
	}
	a := &service.Account{ID: id}
	var credentials, extra []byte
	if !rows.Next() {
		queryErr := rows.Err()
		_ = rows.Close()
		if queryErr != nil {
			return queryErr
		}
		return service.ErrAccountNotFound
	}
	err = rows.Scan(&a.Platform, &a.Type, &credentials, &extra)
	_ = rows.Close()
	if err != nil {
		return err
	}
	if json.Unmarshal(credentials, &a.Credentials) != nil || json.Unmarshal(extra, &a.Extra) != nil {
		return errors.New("invalid account JSON")
	}
	updates, err := mutate(a)
	if err != nil {
		return err
	}
	for key := range updates {
		if key != service.CodexTurnStatePoolKey && key != service.CodexTurnStateSummaryKey && key != service.CodexTurnStateObservationKey && key != service.CodexTurnStateHuntKey {
			return errors.New("unmanaged turn state key")
		}
	}
	if len(updates) > 0 {
		payload, err := json.Marshal(updates)
		if err != nil {
			return err
		}
		if _, err = tx.Client().ExecContext(ctx, "UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)||$1::jsonb,updated_at=NOW() WHERE id=$2", string(payload), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func stripCodexTurnStateManagedExtra(extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra))
	for k, v := range extra {
		if k != service.CodexTurnStatePoolKey && k != service.CodexTurnStateObservationKey && k != service.CodexTurnStateSummaryKey && k != service.CodexTurnStateHuntKey && k != service.CodexTurnStateModelHoldsKey {
			out[k] = v
		}
	}
	return out
}
