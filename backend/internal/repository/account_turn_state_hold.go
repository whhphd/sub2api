package repository

import (
	"context"
	"encoding/json"
	"errors"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"strings"
	"time"
)

var _ service.CodexTurnStateHoldStore = (*accountRepository)(nil)
var _ service.TurnStateHoldReleaser = (*accountRepository)(nil)

// Lock the same policy row that the settings CAS updates. Once an OFF save has
// committed, an in-flight request with an old policy cannot insert another hold.
func turnStateHoldPolicyLocked(ctx context.Context, tx *dbent.Tx) (bool, error) {
	rows, err := tx.Client().QueryContext(ctx, "SELECT value FROM settings WHERE key=$1 FOR SHARE", service.SettingKeyOpenAIOAuthRuntimeSettings)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return false, rows.Err()
	}
	var raw string
	if err = rows.Scan(&raw); err != nil {
		return false, err
	}
	var p service.OpenAIOAuthRuntimeSettings
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return false, err
	}
	return p.TurnStateAutoEnabled && p.TurnStateHunter.Enabled && p.TurnStateHunter.HoldWhenDegraded, nil
}

func (r *accountRepository) CompareAndSwapTurnStateHold(ctx context.Context, expected *service.Account, until *time.Time, reason string) (bool, error) {
	if expected == nil || expected.Platform != service.PlatformOpenAI || expected.Type != service.AccountTypeOAuth {
		return false, errors.New("invalid turn-state account")
	}
	if until == nil && !strings.HasPrefix(expected.TempUnschedulableReason, "turn_state_hold:") {
		return false, errors.New("cannot release another pause")
	}
	if until != nil && !strings.HasPrefix(reason, "turn_state_hold:") {
		return false, errors.New("invalid hold reason")
	}
	credentials, err := json.Marshal(expected.Credentials)
	if err != nil {
		return false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if until != nil {
		enabled, e := turnStateHoldPolicyLocked(ctx, tx)
		if e != nil || !enabled {
			return false, e
		}
	}
	result, err := tx.Client().ExecContext(ctx, `WITH changed AS (
 UPDATE accounts SET temp_unschedulable_until=$1, temp_unschedulable_reason=NULLIF($2,''), updated_at=NOW()
 WHERE id=$3 AND deleted_at IS NULL AND platform='openai' AND type='oauth'
 AND temp_unschedulable_until IS NOT DISTINCT FROM $4::timestamptz
 AND COALESCE(temp_unschedulable_reason,'')=$5 AND credentials=$6::jsonb
 AND ($1::timestamptz IS NULL OR ((temp_unschedulable_until IS NULL OR temp_unschedulable_until<=NOW() OR temp_unschedulable_reason LIKE 'turn_state_hold:%')
 AND status='active' AND schedulable IS TRUE
 AND (rate_limit_reset_at IS NULL OR rate_limit_reset_at<=NOW())
 AND (overload_until IS NULL OR overload_until<=NOW()))) RETURNING id)
 INSERT INTO scheduler_outbox(event_type,account_id) SELECT $7,id FROM changed`, until, reason, expected.ID, expected.TempUnschedulableUntil, expected.TempUnschedulableReason, string(credentials), service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, expected.ID)
	return true, nil
}

func (r *accountRepository) ReleaseTurnStateHoldsIfDisabled(ctx context.Context) (int, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	enabled, err := turnStateHoldPolicyLocked(ctx, tx)
	if err != nil {
		return 0, err
	}
	if enabled {
		return 0, errors.New("hold was enabled concurrently; reload settings")
	}
	result, err := tx.Client().ExecContext(ctx, `WITH released AS (
 UPDATE accounts SET temp_unschedulable_until=NULL,temp_unschedulable_reason=NULL,updated_at=NOW()
 WHERE platform='openai' AND type='oauth' AND deleted_at IS NULL AND temp_unschedulable_reason LIKE 'turn_state_hold:%' RETURNING id)
 INSERT INTO scheduler_outbox(event_type,account_id) SELECT $1,id FROM released`, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	// Refresh even previously released accounts: retrying an OFF save after a
	// Redis failure must repair stale snapshots, not just update new DB rows.
	if r.schedulerCache != nil {
		accounts, e := r.ListByPlatform(ctx, service.PlatformOpenAI)
		if e != nil {
			return int(n), e
		}
		for i := range accounts {
			if accounts[i].Type != service.AccountTypeOAuth {
				continue
			}
			if e = r.schedulerCache.SetAccount(ctx, &accounts[i]); e != nil {
				return int(n), e
			}
		}
	}
	return int(n), nil
}
