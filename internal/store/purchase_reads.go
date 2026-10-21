package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	paymentapi "github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

// InvoiceReadOptions carries route-level permissions; customer ownership and
// administrator customer scope are rechecked by ReadInvoice in its transaction.
type InvoiceReadOptions struct {
	Admin           bool
	CanClosePayment bool
	CanVerifyClose  bool
	PaymentFallback PaymentConfiguration
}

const topupRetryProjection = `i.status='unpaid' AND EXISTS(SELECT 1 FROM balance_topups t WHERE t.invoice_id=i.id AND t.status='accepted') AND EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status='closed') AND NOT EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status<>'closed') AND NOT EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AND NOT EXISTS(SELECT 1 FROM balance_topup_settlements s WHERE s.invoice_id=i.id)`
const invoicePendingProjection = `EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS payment_close_anomaly,
 ` + topupRetryProjection + ` AS topup_can_retry,
(SELECT t.status FROM balance_topups t WHERE t.invoice_id=i.id) AS topup_status,
 (SELECT s.amount_cents FROM balance_topup_settlements s WHERE s.invoice_id=i.id AND s.status='pending_liability' AND NOT EXISTS(SELECT 1 FROM balance_topup_liability_resolutions r WHERE r.topup_id=s.topup_id)) AS topup_pending_liability_cents,
 (i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired'))) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS payment_pending,
 EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded') AS external_payment_paid,
 (i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired') AND pt.provider<>'mock')) OR EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id)) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS real_payment_pending, (SELECT jsonb_build_object('remaining_cents',cp.remaining_cents,'status',cp.status,'amount_cents',cp.amount_cents) FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id) AS credit_payment`
const maskedPurchaseFields = `coalesce((SELECT jsonb_agg(CASE WHEN field->>'encrypted'='true' THEN jsonb_set(field,'{value}','"********"'::jsonb) ELSE field END) FROM jsonb_array_elements(purchase_fields) field),'[]'::jsonb)`
const orderItemProjection = `id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,` + maskedPurchaseFields + ` AS purchase_fields,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit`

func collectPurchaseReadRows(rows pgx.Rows, err error) ([]map[string]any, error) {
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if items == nil && err == nil {
		items = []map[string]any{}
	}
	return items, err
}

func (s *Store) ReadCustomerOrder(ctx context.Context, uid, id int64) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT o.version,o.id,o.order_no,o.status,o.review_status,o.total_cents,o.currency,o.promo_code,o.discount_cents,o.created_at,o.updated_at,
 i.id AS invoice_id,i.invoice_no,i.status AS invoice_status,i.amount_cents,i.paid_at,i.due_at
 FROM orders o LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE o.id=$1 AND o.user_id=$2 AND o.deleted_at IS NULL`, id, uid)
	if err != nil {
		return nil, err
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	items, err := collectPurchaseReadRows(tx.Query(ctx, "SELECT "+orderItemProjection+" FROM order_items WHERE order_id=$1 ORDER BY id", id))
	if err != nil {
		return nil, err
	}
	services, err := collectPurchaseReadRows(tx.Query(ctx, `SELECT id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,unit_number,created_at
 FROM services WHERE order_id=$1 AND user_id=$2 ORDER BY id`, id, uid))
	if err != nil {
		return nil, err
	}
	if err = enrichPurchaseReadPricing(ctx, tx, order, items); err != nil {
		return nil, err
	}
	order["items"], order["services"] = items, services
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return order, nil
}

func (s *Store) ReadAdminOrder(ctx context.Context, actor, id int64) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = LockAdminResourceCustomer(ctx, tx, actor, "order", id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT external_refund_order_hold_json(o.id) AS external_refund_order_hold,o.id,o.order_no,o.user_id,u.username,u.email,u.display_name,
COALESCE((SELECT cp.admin_note FROM customer_profiles cp WHERE cp.customer_id=o.user_id),'') AS customer_note,o.invoice_deferred,o.payment_provider,o.payment_channel_instance_id,o.send_order_email,o.email_status,o.version,o.status,o.review_status,o.notes,o.total_cents,o.promo_code,o.discount_cents,o.currency,o.created_at,o.updated_at,
i.id AS invoice_id,i.invoice_no,i.status AS invoice_status,i.amount_cents,i.paid_at,i.due_at
FROM orders o JOIN account_identities u ON u.id=o.user_id LEFT JOIN invoices i ON i.order_id=o.id WHERE o.id=$1 AND o.deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	order, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	itemsRows, err := tx.Query(ctx, `SELECT id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,first_price_override_cents,renewal_price_override_cents,config_options,option_quantities,config_snapshot,`+maskedPurchaseFields+` AS purchase_fields,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit
FROM order_items WHERE order_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(itemsRows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	servicesRows, err := tx.Query(ctx, `SELECT id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,username,upstream_id,last_error,provision_mode,unit_number,version,created_at,updated_at
FROM services WHERE order_id=$1 AND admin_customer_allowed($2,user_id) ORDER BY id`, id, actor)
	if err != nil {
		return nil, err
	}
	services, err := pgx.CollectRows(servicesRows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	if services == nil {
		services = []map[string]any{}
	}
	if err = enrichPurchaseReadPricing(ctx, tx, order, items); err != nil {
		return nil, err
	}
	order["items"], order["services"] = items, services

	return order, tx.Commit(ctx)
}

func (s *Store) ReadInvoice(ctx context.Context, actor, id int64, options InvoiceReadOptions) (map[string]any, error) {
	admin := options.Admin
	isolation := pgx.RepeatableRead
	if admin {
		// Recheck sales ownership using a fresh snapshot after the scope lock.
		isolation = pgx.ReadCommitted
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if admin {
		if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", id); err != nil {
			return nil, err
		}
	}
	projection := `i.id,i.invoice_no,i.user_id,o.id AS order_id,o.order_no,i.status,i.amount_cents,i.due_at,i.paid_at,i.created_at,i.updated_at,i.version,i.invoice_kind,
 i.import_review_required,coalesce(o.promo_code,(SELECT ii.renewal_promo_code FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.renewal_promo_code<>'' LIMIT 1),'') AS promo_code,
 coalesce(o.discount_cents,(SELECT sum(ii.renewal_discount_cents) FROM invoice_items ii WHERE ii.invoice_id=i.id),0) AS discount_cents,` + invoicePendingProjection
	if admin {
		projection += ",u.display_name AS customer_name,u.email"
	}
	query := "SELECT " + projection + ` FROM invoices i JOIN account_identities u ON u.id=i.user_id
 LEFT JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id WHERE i.id=$1 AND ($2::boolean OR i.user_id=$3)`
	if admin {
		query += " FOR SHARE OF i"
	}
	rows, err := tx.Query(ctx, query, id, admin, actor)
	if err != nil {
		return nil, err
	}
	invoice, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	var topupJSON []byte
	if invoice["invoice_kind"] == "topup" {
		err = tx.QueryRow(ctx, `SELECT jsonb_build_object('status',t.status,'settings_version',t.settings_version,'accepted_at',t.accepted_at,'failure_code',coalesce(s.failure_code,''),'payment_id',s.payment_id,'ledger_id',s.ledger_id,'amount_cents',t.amount_cents,'resolution',(SELECT jsonb_build_object('amount_cents',r.amount_cents,'ledger_id',r.ledger_id,'created_at',r.created_at) FROM balance_topup_liability_resolutions r WHERE r.topup_id=t.id)) FROM balance_topups t LEFT JOIN balance_topup_settlements s ON s.topup_id=t.id WHERE t.invoice_id=$1`, id).Scan(&topupJSON)
		if err != nil {
			return nil, err
		}
		var topup any
		if err = json.Unmarshal(topupJSON, &topup); err != nil {
			return nil, err
		}
		invoice["topup"] = topup
	}
	items, err := collectPurchaseReadRows(tx.Query(ctx, `SELECT ii.id,ii.description,ii.item_type,ii.amount_cents,ii.renewal_base_cents,ii.renewal_discount_cents,ii.renewal_discount_percent,ii.renewal_promo_code,ii.renewal_fixed_discount_cents,ii.renewal_discount_limit,ii.renewal_discount_ordinal,ii.upgrade_base_cents,ii.upgrade_discount_cents,ii.upgrade_promo_code,s.id AS service_id,s.name AS service_name
 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id LEFT JOIN services s ON s.id=ii.service_id AND s.user_id=i.user_id
 WHERE ii.invoice_id=$1 ORDER BY ii.id`, id))
	if err != nil {
		return nil, err
	}
	invoiceOrderProjection := strings.Replace(orderItemProjection, maskedPurchaseFields+` AS purchase_fields`, `jsonb_path_query_array(purchase_fields,'$[*] ? (@.show_invoice == true)') AS purchase_fields`, 1)
	orderItems, err := collectPurchaseReadRows(tx.Query(ctx, "SELECT "+invoiceOrderProjection+` FROM order_items WHERE order_id IN
 (SELECT o.id FROM orders o JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id WHERE i.id=$1) ORDER BY id`, id))
	if err != nil {
		return nil, err
	}
	payments, err := collectPurchaseReadRows(tx.Query(ctx, `SELECT pt.id,pt.provider,pt.provider_payment_id,pt.external_transaction_id,CASE WHEN $2::boolean THEN '' ELSE pt.checkout_url END AS checkout_url,pt.amount_cents,pt.status,pt.created_at,pt.completed_at,pt.expires_at,
 (pt.status='expired' OR pt.status='pending' AND pt.expires_at<=clock_timestamp()) AS expired,
 (SELECT c.status FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS check_status,
 (SELECT c.result_code FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS check_code,
 (SELECT c.started_at FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS checked_at,
 (SELECT c.source FROM payment_reconciliation_checks c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS check_source,
 (SELECT c.id FROM payment_close_operations c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS close_operation_id,
 (SELECT c.operation_id FROM payment_close_operations c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS close_operation_external_id,
 EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.payment_id=pt.id AND payment_close_operation_unresolved(c.id) AND (c.status='unknown' OR c.lease_until<=clock_timestamp())) AS close_verification_required,
 EXISTS(SELECT 1 FROM payment_close_verification_requests r WHERE r.payment_id=pt.id AND r.lease_until>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM payment_close_verification_results v WHERE v.request_id=r.id)) OR EXISTS(SELECT 1 FROM payment_reconciliation_checks q WHERE q.payment_id=pt.id AND q.status='checking' AND q.lease_until>clock_timestamp()) AS close_verification_busy,
 (SELECT c.status FROM payment_close_operations c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS close_status,
 (SELECT c.result_code FROM payment_close_operations c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS close_code,
 (SELECT c.started_at FROM payment_close_operations c WHERE c.payment_id=pt.id ORDER BY c.id DESC LIMIT 1) AS close_requested_at,
 EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.payment_id=pt.id AND payment_close_anomaly_unresolved(a.id)) AS close_anomaly,
 EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.payment_id=pt.id AND payment_close_operation_unresolved(c.id)) AS close_frozen,
 pt.next_reconcile_at,pt.auto_reconcile_attempts,
 pt.status IN ('pending','expired') AND pt.provider=ANY($3::text[]) AND pt.auto_reconcile_attempts>=32 AND pt.next_reconcile_at IS NULL AS reconciliation_exhausted
 FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id AND i.user_id=pt.user_id WHERE i.id=$1 ORDER BY pt.id DESC LIMIT 50`, id, admin, paymentapi.QueryMethods()))
	if err != nil {
		return nil, err
	}

	queryMethods := paymentapi.QueryMethods()
	for _, payment := range payments {
		gateway, enabled, gatewayErr := s.PaymentTransactionGatewayTx(ctx, tx, payment["provider"].(string), payment["provider_payment_id"].(string), options.PaymentFallback)
		_, querySupported := gateway.(paymentapi.QueryGateway)
		payment["query_enabled"] = gatewayErr == nil && enabled && querySupported && slices.Contains(queryMethods, payment["provider"].(string))
		_, closeSupported := gateway.(paymentapi.CloseGateway)
		payment["close_supported"] = gatewayErr == nil && enabled && closeSupported
		_, lookupSupported := gateway.(paymentapi.CloseStatusGateway)
		var snapshotPresent bool
		if err = tx.QueryRow(ctx, "SELECT octet_length(credential_snapshot)>0 FROM payment_transactions WHERE id=$1", payment["id"]).Scan(&snapshotPresent); err != nil {
			return nil, err
		}
		payment["lookup_supported"] = gatewayErr == nil && enabled && lookupSupported && snapshotPresent
		payment["close_verification_can_request"] = admin && options.CanVerifyClose && payment["lookup_supported"] == true && payment["close_verification_required"] == true && payment["close_verification_busy"] != true && invoice["payment_close_anomaly"] != true && invoice["status"] == "unpaid"
		resolution, resolutionErr := s.PaymentCloseResolutionTx(ctx, tx, payment["id"].(int64))
		if resolutionErr != nil {
			return nil, resolutionErr
		}
		payment["close_resolution"] = resolution
		payment["effective_close_status"] = payment["close_status"]
		if resolution != nil {
			payment["effective_close_status"] = resolution.PaymentStatus
		}
		payment["close_enabled"] = admin && options.CanClosePayment && gatewayErr == nil && enabled && closeSupported && (payment["status"] == "pending" || payment["status"] == "expired") && payment["close_frozen"] != true && invoice["payment_close_anomaly"] != true
	}
	ledger, err := collectPurchaseReadRows(tx.Query(ctx, `SELECT l.id,l.entry_type,l.delta_cents,l.balance_after,l.created_at
 FROM balance_ledger l JOIN invoices i ON i.id=l.reference_id AND i.user_id=l.user_id
 WHERE l.reference_type IN ('invoice','gateway_topup') AND i.id=$1 ORDER BY l.id DESC LIMIT 50`, id))
	if err != nil {
		return nil, err
	}
	if err = enrichPurchaseReadPricing(ctx, tx, invoice, orderItems); err != nil {
		return nil, err
	}
	invoice["items"], invoice["order_items"], invoice["payments"], invoice["ledger"] = items, orderItems, payments, ledger
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return invoice, nil
}

func (s *Store) ReadCart(ctx context.Context, userID int64) (map[string]any, error) {
	// Hold the cart row and pricing/catalog locks so version and items stay consistent.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var cartID string
	var version int64
	if err = tx.QueryRow(ctx, "SELECT id::text,version FROM carts WHERE user_id=$1 FOR SHARE", userID).Scan(&cartID, &version); err != nil {
		return nil, err
	}
	if err = GroupPricingReadLock(ctx, tx); err != nil {
		return nil, err
	}
	if err = CatalogReadLock(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT ci.id,ci.product_id,ci.quantity,p.name,p.slug,ci.billing_cycle,ci.option_ids,ci.option_ids::text AS option_ids_json,ci.option_quantities::text AS option_quantities_json,ci.purchase_fields,ci.purchase_fields::text AS purchase_fields_json,pp.price_cents AS base_price_cents,pp.setup_fee_cents AS base_setup_cents,
 pp.price_cents+COALESCE((SELECT sum(COALESCE(pop.price_cents,po.price_delta_cents)) FROM product_options po LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=ci.billing_cycle AND pop.active WHERE po.id=ANY(SELECT jsonb_array_elements_text(ci.option_ids)::bigint)),0) AS price_cents,
 pp.setup_fee_cents+COALESCE((SELECT sum(COALESCE(pop.setup_fee_cents,po.setup_delta_cents)) FROM product_options po LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=ci.billing_cycle AND pop.active WHERE po.id=ANY(SELECT jsonb_array_elements_text(ci.option_ids)::bigint)),0) AS setup_fee_cents,
 p.stock,(p.active AND pp.active AND catalog_category_visible(p.category_id)) AS active,(ci.product_revision=p.revision) AS current,
 (pp.price_cents+pp.setup_fee_cents+COALESCE((SELECT sum(COALESCE(pop.price_cents,po.price_delta_cents)+COALESCE(pop.setup_fee_cents,po.setup_delta_cents)) FROM product_options po LEFT JOIN product_option_prices pop ON pop.option_id=po.id AND pop.billing_cycle=ci.billing_cycle AND pop.active WHERE po.id=ANY(SELECT jsonb_array_elements_text(ci.option_ids)::bigint)),0))*ci.quantity AS total_cents FROM cart_items ci JOIN products p ON p.id=ci.product_id JOIN product_prices pp ON pp.product_id=ci.product_id AND pp.billing_cycle=ci.billing_cycle WHERE ci.cart_id=$1 ORDER BY ci.id`, cartID)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []map[string]any{}
	}
	rules, err := loadGroupPricing(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	var groupDiscount int64
	for _, item := range items {
		if err = s.CheckOrderAdmission(ctx, tx, userID, item["product_id"].(int64), false); err != nil {
			if c, ok := err.(*Conflict); ok {
				item["active"] = false
				item["admission_error"] = c.Message
				item["admission_code"] = c.Code
			} else {
				return nil, err
			}
		}
		var ids []int64
		if err = json.Unmarshal([]byte(item["option_ids_json"].(string)), &ids); err != nil {
			return nil, err
		}
		quantities := map[int64]int64{}
		if err = json.Unmarshal([]byte(item["option_quantities_json"].(string)), &quantities); err != nil {
			return nil, err
		}
		selection, selectionErr := ValidateProductOptionsForCycleWithQuantities(ctx, tx, item["product_id"].(int64), item["billing_cycle"].(string), ids, quantities)
		if selectionErr == nil {
			price, setup, amountErr := OptionAmounts(item["base_price_cents"].(int64), item["base_setup_cents"].(int64), selection)
			selectionErr = amountErr
			if selectionErr == nil {
				item["price_cents"], item["setup_fee_cents"] = price, setup
				item["total_cents"] = (price + setup) * purchaseReadInteger(item["quantity"])
			}
		}
		if selectionErr != nil {
			if _, ok := selectionErr.(*Conflict); !ok {
				return nil, selectionErr
			}
			item["active"] = false
		}
		item["options_valid"] = selectionErr == nil
		item["config_snapshot"] = selection.Snapshot
		item["option_quantities"] = selection.Quantities
		var fieldValues map[string]string
		if err = json.Unmarshal([]byte(item["purchase_fields_json"].(string)), &fieldValues); err != nil {
			return nil, err
		}
		canonicalFields, fieldSnapshot, fieldErr := ValidateStoredPurchaseFields(ctx, tx, userID, item["product_id"].(int64), fieldValues, s.PurchaseFieldEncryptionKey)
		if fieldErr != nil {
			if _, ok := fieldErr.(*Conflict); !ok {
				return nil, fieldErr
			}
			item["active"] = false
			item["fields_error"] = fieldErr.Error()
		}
		item["fields_valid"] = fieldErr == nil
		item["purchase_field_snapshot"] = fieldSnapshot
		if fieldErr == nil {
			for i := range fieldSnapshot {
				if fieldSnapshot[i].Encrypted {
					fieldSnapshot[i].Value = "********"
					canonicalFields[fieldSnapshot[i].Key] = "********"
				}
			}
			item["purchase_fields"] = canonicalFields
			item["purchase_field_snapshot"] = fieldSnapshot
		} else {
			item["purchase_fields"] = map[string]string{}
		}
		if selectionErr == nil && rules.enabled {
			p, _, pricingErr := rules.price(checkoutLine{ID: purchaseReadInteger(item["id"]), ProductID: purchaseReadInteger(item["product_id"]), Qty: int(purchaseReadInteger(item["quantity"])), Cycle: item["billing_cycle"].(string), UnitPrice: purchaseReadInteger(item["price_cents"]), UnitSetup: purchaseReadInteger(item["setup_fee_cents"]), DefaultRecurring: purchaseReadInteger(item["price_cents"])})
			if pricingErr != nil {
				return nil, pricingErr
			}
			item["pricing"] = p
			groupDiscount += p.GroupDiscountCents
		}
		delete(item, "purchase_fields_json")
		delete(item, "option_ids_json")
		delete(item, "option_quantities_json")
		delete(item, "base_price_cents")
		delete(item, "base_setup_cents")
	}
	var promoCode string
	var promoID int64
	if err = tx.QueryRow(ctx, "SELECT promo_code_id,code FROM cart_promo_codes WHERE cart_id=$1", cartID).Scan(&promoID, &promoCode); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	var promo any
	var discountCents int64
	if promoCode != "" {
		quote, subtotal, quoteErr := s.CartPromoQuote(ctx, tx, userID, cartID, promoCode, false)
		if quoteErr == nil {
			discountCents = quote.DiscountCents
			promo = map[string]any{"id": promoID, "code": quote.Code, "discount_type": quote.DiscountType, "discount_cents": quote.DiscountCents, "subtotal_cents": subtotal, "total_cents": subtotal - quote.DiscountCents, "recurring": quote.Recurring, "recurring_limit": quote.RecurringLimit}
		} else {
			promo = map[string]any{"id": promoID, "code": promoCode, "discount_cents": 0, "error": quoteErr.Error()}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"id": cartID, "version": version, "currency": "CNY", "items": items, "promo": promo, "discount_cents": discountCents + groupDiscount, "group_discount_cents": groupDiscount, "promo_discount_cents": discountCents}, nil
}

func purchaseReadInteger(value any) int64 {
	switch n := value.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}
