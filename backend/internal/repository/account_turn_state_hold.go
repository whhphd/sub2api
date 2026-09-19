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
func turnStateHoldPolicyLocked(ctx context.Context, tx *dbent.Tx, exemptions ...*[]string) (bool, error) {
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
	p := *service.DefaultOpenAIOAuthRuntimeSettings(false)
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return false, err
	}
	if len(exemptions) > 0 && exemptions[0] != nil {
		*exemptions[0] = p.TurnStateHunter.HoldExcludedModels
	}
	return p.TurnStateAutoEnabled && p.TurnStateHunter.Enabled && p.TurnStateHunter.HoldWhenDegraded, nil
}

func (r *accountRepository) CompareAndSwapTurnStateHold(ctx context.Context, expected *service.Account, until *time.Time, reason string) (bool, error) {
	if expected == nil || expected.Platform != service.PlatformOpenAI || expected.Type != service.AccountTypeOAuth {
		return false, errors.New("invalid turn-state account")
	}
	model, ok := strings.CutPrefix(reason, "turn_state_hold:")
	if !ok || strings.TrimSpace(model) == "" || len(model) > 256 {
		return false, errors.New("invalid hold model")
	}
	credentials, err := json.Marshal(expected.Credentials)
	if err != nil {
		return false, err
	}
	holds, _ := expected.Extra[service.CodexTurnStateModelHoldsKey].(map[string]any)
	old, err := json.Marshal(holds[model])
	if err != nil {
		return false, err
	}
	var next any
	if until != nil {
		next = until.UTC().Format(time.RFC3339Nano)
	}
	value, err := json.Marshal(next)
	if err != nil {
		return false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if until != nil {
		var excluded []string
		enabled, e := turnStateHoldPolicyLocked(ctx, tx, &excluded)
		exempt := false
		for _, v := range excluded {
			if v == model {
				exempt = true
			}
		}
		if e != nil || !enabled || exempt {
			return false, e
		}
	}
	result, err := tx.Client().ExecContext(ctx, `WITH changed AS (
 UPDATE accounts SET extra=jsonb_set(COALESCE(extra,'{}'::jsonb),'{openai_turn_state_model_holds}',
 CASE WHEN $1::boolean THEN COALESCE(extra->'openai_turn_state_model_holds','{}'::jsonb)-$2::text
 ELSE COALESCE(extra->'openai_turn_state_model_holds','{}'::jsonb)||jsonb_build_object($2::text,$3::jsonb) END),
 temp_unschedulable_until=CASE WHEN temp_unschedulable_reason=$4 THEN NULL ELSE temp_unschedulable_until END,
 temp_unschedulable_reason=CASE WHEN temp_unschedulable_reason=$4 THEN NULL ELSE temp_unschedulable_reason END,updated_at=NOW()
 WHERE id=$5 AND deleted_at IS NULL AND platform='openai' AND type='oauth' AND credentials=$6::jsonb
 AND COALESCE(extra->'openai_turn_state_model_holds'->$2::text,'null'::jsonb)=$7::jsonb
 AND ($1::boolean OR (SELECT count(*) FROM jsonb_object_keys(COALESCE(extra->'openai_turn_state_model_holds','{}'::jsonb)))<16 OR (extra->'openai_turn_state_model_holds') ? $2::text)
 AND (temp_unschedulable_reason IS DISTINCT FROM $4 OR (temp_unschedulable_until IS NOT DISTINCT FROM $8::timestamptz AND COALESCE(temp_unschedulable_reason,'')=$9))
 AND (NOT $1::boolean OR (extra->'openai_turn_state_model_holds') ? $2::text OR temp_unschedulable_reason=$4)
 RETURNING id)
 INSERT INTO scheduler_outbox(event_type,account_id) SELECT $10,id FROM changed`, until == nil, model, string(value), "turn_state_hold:"+model, expected.ID, string(credentials), string(old), expected.TempUnschedulableUntil, expected.TempUnschedulableReason, service.SchedulerOutboxEventAccountChanged)
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
	var excluded []string
	enabled, err := turnStateHoldPolicyLocked(ctx, tx, &excluded)
	if err != nil {
		return 0, err
	}
	if enabled && len(excluded) == 0 {
		return 0, errors.New("hold policy changed concurrently; reload settings")
	}
	exclusions, _ := json.Marshal(excluded)
	result, err := tx.Client().ExecContext(ctx, `WITH targets AS (
 SELECT id, COALESCE((SELECT jsonb_object_agg(key,value) FROM jsonb_each(COALESCE(extra->'openai_turn_state_model_holds','{}'::jsonb)) WHERE $2::boolean AND NOT ($3::jsonb ? key)),'{}'::jsonb) AS kept,
 (temp_unschedulable_reason LIKE 'turn_state_hold:%' AND (NOT $2::boolean OR $3::jsonb ? substring(temp_unschedulable_reason from length('turn_state_hold:')+1))) AS clear_legacy
 FROM accounts WHERE platform='openai' AND type='oauth' AND deleted_at IS NULL FOR NO KEY UPDATE
 ), released AS (
 UPDATE accounts a SET extra=jsonb_set(COALESCE(a.extra,'{}'::jsonb),'{openai_turn_state_model_holds}',t.kept),
 temp_unschedulable_until=CASE WHEN t.clear_legacy THEN NULL ELSE a.temp_unschedulable_until END,
 temp_unschedulable_reason=CASE WHEN t.clear_legacy THEN NULL ELSE a.temp_unschedulable_reason END,updated_at=NOW()
 FROM targets t WHERE a.id=t.id AND (t.clear_legacy OR COALESCE(a.extra->'openai_turn_state_model_holds','{}'::jsonb)<>t.kept) RETURNING a.id)
 INSERT INTO scheduler_outbox(event_type,account_id) SELECT $1,id FROM released`, service.SchedulerOutboxEventAccountChanged, enabled, string(exclusions))
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
