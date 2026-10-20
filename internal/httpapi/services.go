package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const serviceColumns = `external_refund_order_hold_json(s.order_id) AS external_refund_order_hold,s.installed_os,s.resource_info,s.id,s.user_id,s.order_id,s.order_item_id,s.product_id,s.name,s.domain,s.billing_cycle,
s.status,s.next_due_at,s.auto_renew,s.auto_renew_error,s.username,s.upstream_id,s.last_error,s.provision_mode,s.unit_number,s.inventory_released,s.version,s.created_at,s.updated_at,
COALESCE((SELECT jsonb_agg(jsonb_build_object('id',sos.id,'group_id',sos.group_id,'group_name',pog.name,'option_id',sos.option_id,'label',po.label,'value',po.value,'quantity',sos.quantity) ORDER BY pog.sort_order,pog.id) FROM service_option_selections sos JOIN product_option_groups pog ON pog.id=sos.group_id LEFT JOIN product_options po ON po.id=sos.option_id WHERE sos.service_id=s.id),'[]'::jsonb) AS config_options`

const serviceGroupColumns = `,coalesce((SELECT ` + maskedPurchaseFields + ` FROM order_items oi WHERE oi.id=s.order_item_id),'[]'::jsonb) AS purchase_fields,p.service_group_id,g.name AS service_group_name,p.revision AS product_revision,p.config_upgrade_enabled,p.config_downgrade_enabled,
EXISTS(SELECT 1 FROM product_upgrade_targets t WHERE t.product_id=s.product_id) AS product_upgrade_available,
coalesce(s.recurring_price_cents,(SELECT oi.price_cents FROM order_items oi WHERE oi.id=s.order_item_id),p.price_cents) AS recurring_price_cents,
s.renewal_discount_percent,s.renewal_promo_code,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used,
coalesce(s.recurring_price_cents,(SELECT oi.price_cents FROM order_items oi WHERE oi.id=s.order_item_id),p.price_cents)
 -service_renewal_discount(coalesce(s.recurring_price_cents,(SELECT oi.price_cents FROM order_items oi WHERE oi.id=s.order_item_id),p.price_cents),s.renewal_discount_percent,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used) AS renewal_amount_cents,
coalesce(s.config_snapshot,(SELECT oi.config_snapshot FROM order_items oi WHERE oi.id=s.order_item_id),'[]'::jsonb) AS config_snapshot,
coalesce(s.option_ids,(SELECT oi.config_options FROM order_items oi WHERE oi.id=s.order_item_id),'[]'::jsonb) AS option_ids,
coalesce(s.option_quantities,(SELECT oi.option_quantities FROM order_items oi WHERE oi.id=s.order_item_id),'{}'::jsonb) AS option_quantities`
const serviceJoins = ` FROM services s JOIN users u ON u.id=s.user_id JOIN products p ON p.id=s.product_id LEFT JOIN service_navigation_groups g ON g.id=p.service_group_id`

func (s *Server) services(w http.ResponseWriter, r *http.Request)     { s.servicesList(w, r, false) }
func (s *Server) servicesPage(w http.ResponseWriter, r *http.Request) { s.servicesList(w, r, true) }
func (s *Server) servicesList(w http.ResponseWriter, r *http.Request, paged bool) {
	var group *int64
	var customerID *int64
	if adminRoute(r) {
		var err error
		customerID, err = s.db.AdminCustomerFilter(r.Context(), identityFrom(r).ID, r.URL.Query())
		if accountBusinessFailure(s, w, r, err) {
			return
		}
	}
	if raw := r.URL.Query().Get("group_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 0 {
			writeError(w, 400, "invalid_service_group", "服务分组筛选无效")
			return
		}
		group = &id
	}
	state := r.URL.Query().Get("status")
	if state != "" && state != "pending_payment" && state != "queued" && state != "provisioning" && state != "active" && state != "suspended" && state != "terminated" && state != "failed" && state != "cancelled" && state != "import_review_required" {
		writeError(w, 400, "invalid_status", "服务状态无效")
		return
	}
	limit, offset := pagination(r)
	result, err := s.db.ServiceAccountList(r.Context(), store.ServiceAccountListFilter{
		Actor: identityFrom(r).ID, Admin: adminRoute(r), Page: paged,
		CustomerID: customerID, GroupID: group, Status: state,
		Query: strings.TrimSpace(r.URL.Query().Get("q")), Limit: limit, Offset: offset,
	})
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}

func (s *Server) serviceDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if adminRoute(r) {
			if err = store.LockAdminResourceCustomer(r.Context(), tx, identityFrom(r).ID, "service", id); err != nil {
				return txResponse{}, err
			}
		}
		rows, err := tx.Query(r.Context(), `SELECT `+serviceColumns+`,u.display_name AS customer_name`+serviceGroupColumns+serviceJoins+`
WHERE s.id=$1 AND (($2::boolean AND admin_customer_allowed($3,s.user_id)) OR (NOT $2::boolean AND s.user_id=$3))`, id, adminRoute(r), identityFrom(r).ID)
		if err != nil {
			return txResponse{}, err
		}
		service, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
		if err != nil {
			return txResponse{}, err
		}
		if service["provision_mode"] == "supplier" {
			caps, capErr := s.db.ServiceSupplierCapabilitiesTx(r.Context(), tx, id, adminRoute(r))
			if capErr != nil {
				return txResponse{}, capErr
			}
			service["supplier_capabilities"] = caps
		}
		if adminRoute(r) {
			metadata, queryErr := tx.Query(r.Context(), `SELECT p.name AS product_name,
 o.order_no,oi.price_cents AS first_price_cents,oi.setup_fee_cents,i.id AS invoice_id,i.invoice_no,i.status AS invoice_status,i.paid_at,
 a.name AS supplier_name,b.upstream_product_id,b.provider AS supplier_provider,
 a.running_job_id AS supplier_running_job_id,j.service_id AS supplier_blocking_service_id,j.action AS supplier_blocking_action
 FROM services s JOIN products p ON p.id=s.product_id JOIN orders o ON o.id=s.order_id
 JOIN order_items oi ON oi.id=s.order_item_id LEFT JOIN invoices i ON i.order_id=s.order_id
 LEFT JOIN supplier_product_bindings b ON b.product_id=s.product_id AND s.provision_mode='supplier'
 LEFT JOIN supplier_accounts a ON a.id=b.supplier_id LEFT JOIN provision_jobs j ON j.id=a.running_job_id WHERE s.id=$1`, id)
			if queryErr != nil {
				return txResponse{}, queryErr
			}
			details, queryErr := pgx.CollectExactlyOneRow(metadata, pgx.RowToMap)
			if queryErr != nil {
				return txResponse{}, queryErr
			}
			for key, value := range details {
				service[key] = value
			}
		}
		jobs, err := tx.Query(r.Context(), `SELECT id,action,status,attempts,available_at,locked_at,completed_at,last_error,
 result-'manual_lifecycle_verification' AS result,request_payload,created_at,
 (request_payload->>'manual_lifecycle'='true' AND action IN ('suspend','resume','terminate')) IS TRUE AS manual_lifecycle
FROM provision_jobs WHERE service_id=$1 ORDER BY id DESC LIMIT 50`, id)
		if err != nil {
			return txResponse{}, err
		}
		jobRows, err := pgx.CollectRows(jobs, pgx.RowToMap)
		if err != nil {
			return txResponse{}, err
		}
		if jobRows == nil {
			jobRows = []map[string]any{}
		}
		service["jobs"] = jobRows
		cancellation, err := cancellationForService(r, s, id)
		if err != nil {
			return txResponse{}, err
		}
		service["cancellation"] = cancellation
		changes, err := tx.Query(r.Context(), `SELECT id,status,change_type,target_product_id,target_product_name,billing_cycle,new_billing_cycle,amount_cents,credit_cents,old_recurring_cents,new_recurring_cents,due_at,new_snapshot AS config_snapshot,invoice_id,job_id,created_at,completed_at FROM service_configuration_changes WHERE service_id=$1 AND invoice_id IS NOT NULL ORDER BY id DESC LIMIT 20`, id)
		if err != nil {
			return txResponse{}, err
		}
		changeRows, err := pgx.CollectRows(changes, pgx.RowToMap)
		if err != nil {
			return txResponse{}, err
		}
		if changeRows == nil {
			changeRows = []map[string]any{}
		}
		service["configuration_changes"] = changeRows

		return txResponse{200, service}, nil
	})
}

func (s *Server) serviceAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Action       string `json:"action"`
		System       int    `json:"system"`
		TempPassword string `json:"temp_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	if input.Action == "" {
		writeError(w, 400, "invalid_service_action", "服务动作不能为空")
		return
	}
	var result store.ServiceActionResult
	var err error
	if input.Action == "rescue_system" {
		result, err = s.db.RescueService(r.Context(), identityFrom(r).ID, id, input.System, input.TempPassword, r.Header.Get("Idempotency-Key"), adminRoute(r))
	} else {
		result, err = s.db.EnqueueServiceAction(r.Context(), identityFrom(r).ID, id, input.Action, r.Header.Get("Idempotency-Key"), adminRoute(r))
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) resetServicePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.ResetServicePassword(r.Context(), identityFrom(r).ID, id, input.Password, r.Header.Get("Idempotency-Key"), adminRoute(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) serviceDelivery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.ManualCompletion
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.CompleteManualServiceJob(requestFileContext(r), identityFrom(r).ID, id, input)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, result)
}
