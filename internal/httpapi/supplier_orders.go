package httpapi

import (
	"fmt"
	"net/http"
	"time"
)

// A supplier order is proven by immutable service delivery snapshots, never
// by the product's current binding. The customer amount remains order-wide.
const supplierOrderProjection = `,
 coalesce((SELECT jsonb_agg(jsonb_build_object('service_id',s.id,'supplier_id',s.delivery_snapshot->>'supplier_id','supplier_name',a.name,'cost_cents',j.result->'cost_cents','currency',j.result->>'currency') ORDER BY s.id)
 FROM services s LEFT JOIN supplier_accounts a ON a.id::text=s.delivery_snapshot->>'supplier_id'
 LEFT JOIN LATERAL (SELECT result FROM provision_jobs WHERE service_id=s.id AND action='create' AND status='succeeded' AND result->>'currency'='CNY' AND result ? 'cost_cents' ORDER BY id DESC LIMIT 1) j ON true
 WHERE s.order_id=o.id AND s.user_id=o.user_id AND s.provision_mode='supplier' AND s.delivery_snapshot->>'supplier_id' ~ '^[1-9][0-9]*$'),'[]'::jsonb) AS supplier_resources`

func supplierOrderWhere(w http.ResponseWriter, r *http.Request, where string, args []any, renew bool) (string, []any, bool) {
	proof := "EXISTS(SELECT 1 FROM services ss WHERE ss.order_id=o.id AND ss.user_id=o.user_id AND ss.provision_mode='supplier' AND ss.delivery_snapshot->>'supplier_id' ~ '^[1-9][0-9]*$'"
	if renew {
		proof = "EXISTS(SELECT 1 FROM services ss WHERE ss.id=ii.service_id AND ss.user_id=i.user_id AND ss.provision_mode='supplier' AND ss.delivery_snapshot->>'supplier_id' ~ '^[1-9][0-9]*$'"
	}
	if raw := r.URL.Query().Get("supplier_id"); raw != "" {
		if _, err := parsePositiveID(raw); err != nil {
			writeError(w, 400, "invalid_supplier", "供应商筛选无效")
			return where, args, false
		}
		args = append(args, raw)
		proof += fmt.Sprintf(" AND ss.delivery_snapshot->>'supplier_id'=$%d", len(args))
	}
	where += " AND " + proof + ")"
	if renew {
		zone := time.FixedZone("Asia/Shanghai", 8*3600)
		var start, end time.Time
		for _, key := range []string{"date_from", "date_to"} {
			if raw := r.URL.Query().Get(key); raw != "" {
				value, err := time.ParseInLocation("2006-01-02", raw, zone)
				if err != nil {
					writeError(w, 400, "invalid_order_date", "日期筛选无效")
					return where, args, false
				}
				operator := ">="
				if key == "date_to" {
					end = value
					value = value.AddDate(0, 0, 1)
					operator = "<"
				} else {
					start = value
				}
				args = append(args, value)
				where += fmt.Sprintf(" AND i.created_at %s $%d", operator, len(args))
			}
		}
		if !start.IsZero() && !end.IsZero() && end.Before(start) {
			writeError(w, 400, "invalid_order_date", "结束日期不能早于开始日期")
			return where, args, false
		}
	}
	return where, args, true
}
