package store

import (
	"context"
	"fmt"
)

const serviceAccountMaskedPurchaseFields = `coalesce((SELECT jsonb_agg(CASE WHEN field->>'encrypted'='true' THEN jsonb_set(field,'{value}','"********"'::jsonb) ELSE field END) FROM jsonb_array_elements(purchase_fields) field),'[]'::jsonb)`
const serviceAccountColumns = `s.installed_os,s.id,s.user_id,service_ownership_generation(s.id) AS owner_generation,s.product_id,s.name,s.domain,s.billing_cycle,
s.status,s.next_due_at,s.auto_renew,s.auto_renew_error,s.username,s.upstream_id,s.provision_mode,s.unit_number,s.inventory_released,s.version,s.created_at,s.updated_at,
COALESCE((SELECT jsonb_agg(jsonb_build_object('id',sos.id,'group_id',sos.group_id,'group_name',pog.name,'option_id',sos.option_id,'label',po.label,'value',po.value,'quantity',sos.quantity) ORDER BY pog.sort_order,pog.id) FROM service_option_selections sos JOIN product_option_groups pog ON pog.id=sos.group_id LEFT JOIN product_options po ON po.id=sos.option_id WHERE sos.service_id=s.id),'[]'::jsonb) AS config_options`

const serviceAccountGroupColumns = `,p.service_group_id,g.name AS service_group_name,p.revision AS product_revision,p.config_upgrade_enabled,p.config_downgrade_enabled,
EXISTS(SELECT 1 FROM product_upgrade_targets t WHERE t.product_id=s.product_id) AS product_upgrade_available,
coalesce(s.recurring_price_cents,(SELECT oi.price_cents FROM order_items oi WHERE oi.id=s.order_item_id),p.price_cents) AS recurring_price_cents,
s.renewal_discount_percent,s.renewal_promo_code,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used,
CASE WHEN EXISTS(SELECT 1 FROM service_renewal_pricing rp WHERE rp.service_id=s.id) THEN 'fixed' ELSE 'contract' END AS renewal_pricing_mode,
(SELECT fixed_amount_cents FROM service_renewal_pricing rp WHERE rp.service_id=s.id) AS fixed_renewal_amount_cents,
coalesce((SELECT fixed_amount_cents FROM service_renewal_pricing rp WHERE rp.service_id=s.id),
 coalesce(s.recurring_price_cents,(SELECT oi.price_cents FROM order_items oi WHERE oi.id=s.order_item_id),p.price_cents)
 -service_renewal_discount(coalesce(s.recurring_price_cents,(SELECT oi.price_cents FROM order_items oi WHERE oi.id=s.order_item_id),p.price_cents),s.renewal_discount_percent,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used)) AS renewal_amount_cents,
coalesce(s.config_snapshot,(SELECT oi.config_snapshot FROM order_items oi WHERE oi.id=s.order_item_id),'[]'::jsonb) AS config_snapshot,
coalesce(s.option_ids,(SELECT oi.config_options FROM order_items oi WHERE oi.id=s.order_item_id),'[]'::jsonb) AS option_ids,
coalesce(s.option_quantities,(SELECT oi.option_quantities FROM order_items oi WHERE oi.id=s.order_item_id),'{}'::jsonb) AS option_quantities`
const serviceAccountJoins = ` FROM services s JOIN users u ON u.id=s.user_id JOIN products p ON p.id=s.product_id LEFT JOIN service_navigation_groups g ON g.id=p.service_group_id`

// Financial ancestry remains private to the original customer. A service that
// returns to that customer still starts a new ownership generation; its old
// purchase input and operational history must not be reopened.
func serviceAccountOwnershipColumns(admin, actor string) string {
	originalVisible := fmt.Sprintf(`EXISTS(SELECT 1 FROM orders original_order WHERE original_order.id=s.order_id AND
 ((%s::boolean AND admin_customer_allowed(%s,original_order.user_id)) OR
 (NOT %s::boolean AND original_order.user_id=s.user_id AND NOT EXISTS(SELECT 1 FROM service_ownership_transfers ownership WHERE ownership.service_id=s.id))))`, admin, actor, admin)
	return fmt.Sprintf(`,
 CASE WHEN %[1]s THEN s.order_id END AS order_id,
 CASE WHEN %[1]s THEN s.order_item_id END AS order_item_id,
 CASE WHEN %[1]s THEN external_refund_order_hold_json(s.order_id) END AS external_refund_order_hold,
 CASE WHEN %[1]s THEN coalesce((SELECT `+serviceAccountMaskedPurchaseFields+` FROM order_items oi WHERE oi.id=s.order_item_id),'[]'::jsonb) ELSE '[]'::jsonb END AS purchase_fields,
 CASE WHEN %[2]s::boolean OR NOT EXISTS(SELECT 1 FROM service_ownership_transfers ownership WHERE ownership.service_id=s.id)
 THEN s.resource_info ELSE coalesce((SELECT jsonb_object_agg(resource.key,resource.value) FROM jsonb_each(s.resource_info) resource
 WHERE resource.key IN ('name','ips','power','dedicated_ip','assigned_ips','port')),'{}'::jsonb) END AS resource_info,
 CASE WHEN %[2]s::boolean OR NOT EXISTS(SELECT 1 FROM service_ownership_transfers ownership WHERE ownership.service_id=s.id)
 THEN s.last_error ELSE '' END AS last_error`, originalVisible, admin)
}

// Each floor belongs to the transfer before the next generation starts. Use
// immutable sequence IDs rather than timestamps or the service's mutable owner.
func serviceOwnershipRecordCustomerSQL(service, recordID, floor string) string {
	return fmt.Sprintf(`coalesce(
 (SELECT ownership.target_customer_id FROM service_ownership_transfers ownership
  WHERE ownership.service_id=%[1]s AND ownership.%[3]s<%[2]s ORDER BY ownership.generation DESC LIMIT 1),
 (SELECT ownership.original_customer_id FROM service_ownership_transfers ownership
  WHERE ownership.service_id=%[1]s ORDER BY ownership.generation LIMIT 1),
 (SELECT ownership_current_service.user_id FROM services ownership_current_service WHERE ownership_current_service.id=%[1]s))`, service, recordID, floor)
}

type ServiceAccountListFilter struct {
	Actor               int64
	Admin, Page         bool
	CustomerID, GroupID *int64
	Status, Query       string
	Limit, Offset       int
}

func (s *Store) ServiceAccountList(ctx context.Context, in ServiceAccountListFilter) (any, error) {
	where := serviceAccountJoins + ` WHERE (($1::boolean AND admin_customer_allowed($2,s.user_id)) OR (NOT $1::boolean AND s.user_id=$2))
 AND ($3::bigint IS NULL OR coalesce(p.service_group_id,0)=$3)
 AND (CASE WHEN $4='import_review_required' THEN s.import_review_required ELSE ($4='' OR s.status=$4) END)
 AND (s.name ILIKE $5 OR s.domain ILIKE $5) AND ($6::bigint IS NULL OR s.user_id=$6)`
	args := []any{in.Admin, in.Actor, in.GroupID, in.Status, "%" + in.Query + "%", in.CustomerID}
	query := "SELECT " + serviceAccountColumns + `,u.display_name AS customer_name` + serviceAccountGroupColumns + serviceAccountOwnershipColumns("$1", "$2") + where
	if in.Page {
		return customerAccountReadPage(ctx, s.Pool, query, "id DESC", args, in.Limit, in.Offset)
	}
	rows, err := s.Pool.Query(ctx, query+" ORDER BY s.id DESC LIMIT $7 OFFSET $8", append(args, in.Limit, in.Offset)...)
	return collectMaps(rows, err)
}
