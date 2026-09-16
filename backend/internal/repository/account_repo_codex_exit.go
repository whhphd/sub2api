package repository

import (
	"context"
	"encoding/json"
	"errors"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// UpdateCodexExitSnapshot uses the same guarded JSONB merge/outbox pattern as
// UpdateUpstreamBillingProbeSnapshot. A proxy edit or newer observation wins
// over any delayed lookup, and unrelated account state is never replaced.
func (r *accountRepository) UpdateCodexExitSnapshot(ctx context.Context, expected *service.Account, updates map[string]any) (bool, error) {
	if dbent.TxFromContext(ctx) != nil {
		return r.updateCodexExitSnapshotInTx(ctx, expected, updates)
	}
	tx, err := r.client.Tx(ctx)
	if errors.Is(err, dbent.ErrTxStarted) {
		return r.updateCodexExitSnapshotInTx(ctx, expected, updates)
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := r.updateCodexExitSnapshotInTx(dbent.NewTxContext(ctx, tx), expected, updates)
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	if changed {
		r.syncSchedulerAccountSnapshot(ctx, expected.ID)
	}
	return changed, nil
}

func (r *accountRepository) updateCodexExitSnapshotInTx(ctx context.Context, expected *service.Account, updates map[string]any) (bool, error) {
	if expected == nil || len(updates) == 0 {
		return false, nil
	}
	payload, err := json.Marshal(updates)
	if err != nil {
		return false, err
	}
	proxyID := expected.ProxyID
	previous := expected.GetExtraString("codex_wire_timezone_resolved_at")
	client := clientFromContext(ctx, r.client)
	matches, err := lockAndMatchProbeProxyIdentity(ctx, client, expected)
	if err != nil || !matches {
		return false, err
	}
	result, err := client.ExecContext(ctx, `
 UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb) || $1::jsonb, updated_at=NOW()
 WHERE id=$2 AND deleted_at IS NULL
 AND proxy_id IS NOT DISTINCT FROM $3::bigint
 AND COALESCE(extra->>'codex_wire_timezone_resolved_at','')=$4`, string(payload), expected.ID, proxyID, previous)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	if err = enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &expected.ID, nil, nil); err != nil {
		return false, err
	}
	return true, nil
}
