package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) ListDueUpstreamBalanceAccounts(ctx context.Context, now time.Time, limit int) ([]service.Account, error) {
	rows, err := r.sql.QueryContext(ctx, `
		WITH candidates AS MATERIALIZED (
			SELECT id, extra #>> '{upstream_balance,next_query_at}' AS next_query_at
			FROM accounts
			WHERE deleted_at IS NULL AND type = 'apikey' AND status = 'active'
		), parsed AS MATERIALIZED (
			SELECT id, next_query_at,
				next_query_at ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$' AS valid_shape,
				jsonb_path_query_first_tz(jsonb_build_object('value', replace(regexp_replace(regexp_replace(
					next_query_at, '(\.[0-9]{6})[0-9]+(Z|[+-][0-9]{2}:[0-9]{2})$', '\1\2'
				), 'Z$', '+00:00'), 'T', ' ')), '$.value.datetime()', '{}'::jsonb, true) #>> '{}' AS parsed_next
			FROM candidates
		)
		SELECT id FROM parsed
		WHERE NOT COALESCE(valid_shape, false) OR parsed_next IS NULL OR parsed_next::timestamptz <= $1
		ORDER BY CASE WHEN valid_shape AND parsed_next IS NOT NULL THEN 1 ELSE 0 END ASC,
		CASE WHEN valid_shape AND parsed_next IS NOT NULL THEN parsed_next::timestamptz END ASC NULLS FIRST, id ASC LIMIT $2
	`, now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accounts, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, *account)
	}
	return result, nil
}

// A stale observation must not replace data from a changed key or newer query.
// Balance snapshots are scheduler-neutral and do not enqueue account changes.
func (r *accountRepository) UpdateUpstreamBalanceSnapshot(ctx context.Context, account *service.Account, snapshot *service.UpstreamBalanceSnapshot) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return err
	}
	expected, err := json.Marshal(account.Extra[service.UpstreamBalanceExtraKey])
	if err != nil {
		return err
	}
	var proxyID any
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	result, err := clientFromContext(ctx, r.client).ExecContext(ctx, `
		UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || jsonb_build_object('upstream_balance', $1::jsonb)
		WHERE id = $2 AND platform = $3 AND type = 'apikey'
			AND credentials = $4::jsonb AND proxy_id IS NOT DISTINCT FROM $5
			AND COALESCE(extra -> 'upstream_balance', 'null'::jsonb) = $6::jsonb
			AND deleted_at IS NULL
	`, string(payload), account.ID, account.Platform, string(credentials), proxyID, string(expected))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return service.ErrUpstreamBalanceIdentityChanged
	}
	return nil
}
