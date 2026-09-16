package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Bound each indexed account/time lookup before joining or merging. This avoids
// counting/scanning the entire day's logs on each account-list refresh.
const opsAccountRecentRequestsSQL = `
SELECT row_to_json(detail) FROM (
 SELECT event.*, u.email AS user_email, a.name AS account_name
 FROM unnest($1::bigint[]) AS requested(account_id)
 CROSS JOIN LATERAL (
  SELECT * FROM (
   (SELECT ul.id, 'success'::text AS kind, ul.created_at, ul.request_id,
     ul.model, ul.upstream_model, ul.duration_ms, ul.first_token_ms,
     NULL::integer AS status_code, NULL::bigint AS error_id,
     NULL::text AS phase, NULL::text AS severity, NULL::text AS message,
     ul.user_id, ul.api_key_id, ul.account_id, ul.group_id, ul.stream,
     CASE ul.request_type WHEN 1 THEN 'sync' WHEN 2 THEN 'stream' WHEN 3 THEN 'ws_v2' WHEN 4 THEN 'cyber' WHEN 5 THEN 'live' ELSE NULL END AS request_type,
     ul.openai_ws_mode, ul.input_tokens, ul.output_tokens,
     ul.cache_read_tokens, ul.cache_creation_tokens, ul.image_input_tokens, ul.image_output_tokens,
     ul.actual_cost, COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1) AS account_cost
    FROM usage_logs ul
    WHERE ul.account_id = requested.account_id AND ul.created_at >= $2 AND ul.created_at < $3
    ORDER BY ul.created_at DESC, ul.id DESC LIMIT 10)
   UNION ALL
   (SELECT o.id, 'error'::text AS kind, o.created_at,
     COALESCE(NULLIF(o.request_id,''), NULLIF(o.client_request_id,''), '') AS request_id,
     o.model, o.upstream_model, o.duration_ms, o.time_to_first_token_ms AS first_token_ms,
     o.status_code, o.id AS error_id, o.error_phase AS phase, o.severity, o.error_message AS message,
     o.user_id, o.api_key_id, o.account_id, o.group_id, o.stream,
     CASE o.request_type WHEN 1 THEN 'sync' WHEN 2 THEN 'stream' WHEN 3 THEN 'ws_v2' WHEN 4 THEN 'cyber' WHEN 5 THEN 'live' ELSE NULL END AS request_type, false AS openai_ws_mode,
     NULL::integer, NULL::integer, NULL::integer, NULL::integer, NULL::integer, NULL::integer,
     NULL::numeric, NULL::numeric
    FROM ops_error_logs o
    WHERE o.account_id = requested.account_id AND o.created_at >= $2 AND o.created_at < $3
      AND COALESCE(o.status_code, 0) >= 400
    ORDER BY o.created_at DESC, o.id DESC LIMIT 10)
  ) combined ORDER BY created_at DESC, id DESC, kind DESC LIMIT 10
 ) event
 LEFT JOIN users u ON u.id = event.user_id
 LEFT JOIN accounts a ON a.id = event.account_id
 ORDER BY event.account_id, event.created_at DESC, event.id DESC, event.kind DESC
) detail`

func (r *opsRepository) GetAccountRecentRequests(ctx context.Context, ids []int64, start, end time.Time) ([]*service.OpsAccountRecentRequest, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	ids, err := service.NormalizeOpsRecentAccountIDs(ids)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, opsAccountRecentRequestsSQL, pq.Array(ids), start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]*service.OpsAccountRecentRequest, 0, len(ids)*service.OpsRecentRequestsPerAccount)
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var item service.OpsAccountRecentRequest
		if err := json.Unmarshal(data, &item); err != nil {
			return nil, err
		}
		out = append(out, &item)
	}
	return out, rows.Err()
}
