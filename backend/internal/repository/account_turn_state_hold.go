package repository

import (
 "context"
 "encoding/json"
 "errors"
 "strings"
 "time"
 "github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.CodexTurnStateHoldStore = (*accountRepository)(nil)

func (r *accountRepository) CompareAndSwapTurnStateHold(ctx context.Context, expected *service.Account, until *time.Time, reason string) (bool,error) {
 if expected == nil || expected.Platform != service.PlatformOpenAI || expected.Type != service.AccountTypeOAuth { return false, errors.New("invalid turn-state account") }
 if until == nil && !strings.HasPrefix(expected.TempUnschedulableReason,"turn_state_hold:") { return false,errors.New("cannot release another pause") }
 if until != nil && !strings.HasPrefix(reason,"turn_state_hold:") { return false,errors.New("invalid hold reason") }
 credentials,err:=json.Marshal(expected.Credentials);if err!=nil {return false,err}
 // CTE keeps the scheduler notification in the same transaction as the update.
 result,err:=r.sql.ExecContext(ctx,`WITH changed AS (
 UPDATE accounts SET temp_unschedulable_until=$1, temp_unschedulable_reason=NULLIF($2,''), updated_at=NOW()
 WHERE id=$3 AND deleted_at IS NULL AND platform='openai' AND type='oauth'
 AND temp_unschedulable_until IS NOT DISTINCT FROM $4::timestamptz
 AND COALESCE(temp_unschedulable_reason,'')=$5 AND credentials=$6::jsonb
 AND ($1::timestamptz IS NULL OR ((temp_unschedulable_until IS NULL OR temp_unschedulable_until<=NOW() OR temp_unschedulable_reason LIKE 'turn_state_hold:%')
 AND status='active' AND schedulable IS TRUE
 AND (rate_limit_reset_at IS NULL OR rate_limit_reset_at<=NOW())
 AND (overload_until IS NULL OR overload_until<=NOW())))
 RETURNING id)
 INSERT INTO scheduler_outbox(event_type,account_id) SELECT $7,id FROM changed`,until,reason,expected.ID,expected.TempUnschedulableUntil,expected.TempUnschedulableReason,string(credentials),service.SchedulerOutboxEventAccountChanged)
 if err!=nil{return false,err};n,err:=result.RowsAffected();if err!=nil||n==0{return false,err}
 r.syncSchedulerAccountSnapshotDetached(ctx,expected.ID)
 return true,nil
}
