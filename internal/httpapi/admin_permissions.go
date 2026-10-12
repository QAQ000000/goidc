package httpapi

import (
	"net/http"
	"slices"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

// Unknown administrative routes deny access, including for super administrators.
// Every new route must make its authority explicit here.
var adminRoutePermissions = func() map[string]string {
	m := map[string]string{}
	m["GET /api/v1/admin/supplier-orders"] = "orders.view"
	m["GET /api/v1/admin/supplier-renewals"] = "orders.view"
	for _, p := range []string{"GET /api/v1/admin/supplier-providers", "GET /api/v1/admin/suppliers", "GET /api/v1/admin/products/{id}/supplier-binding"} {
		m[p] = "products.view"
	}
	for _, p := range []string{"POST /api/v1/admin/suppliers", "PUT /api/v1/admin/suppliers/{id}", "POST /api/v1/admin/suppliers/{id}/catalog", "GET /api/v1/admin/suppliers/{id}/products/{product_id}", "POST /api/v1/admin/suppliers/{id}/import"} {
		m[p] = "products.manage"
	}
	m["POST /api/v1/admin/services/{id}/supplier-verification"] = "services.manage"
	m["GET /api/v1/admin/supplier-jobs"] = "products.view"
	m["GET /api/v1/admin/supplier-tasks"] = "jobs.view"
	m["POST /api/v1/admin/supplier-jobs/{id}/retry"] = "services.manage"
	m["GET /api/v1/admin/provision-profiles"] = "products.view"
	m["POST /api/v1/admin/services/{id}/delivery-verification"] = "services.manage"
	m["POST /api/v1/admin/services/{id}/credentials"] = "services.manage"
	m["POST /api/v1/admin/services/{id}/vnc"] = "services.manage"
	m["POST /api/v1/admin/services/{id}/reset-password"] = "services.manage"
	m["POST /api/v1/admin/services/{id}/reinstall"] = "services.manage"
	m["GET /api/v1/admin/services/{id}/reinstall-templates"] = "services.view"
	m["GET /api/v1/admin/services/{id}/monitoring"] = "services.view"
	m["GET /api/v1/admin/services/{id}/activity"] = "services.view"
	m["GET /api/v1/admin/services/{id}/storage/{kind}"] = "services.view"
	m["POST /api/v1/admin/services/{id}/storage/{kind}"] = "services.manage"
	m["GET /api/v1/admin/services/{id}/nat/{kind}"] = "services.view"
	m["POST /api/v1/admin/services/{id}/nat/{kind}"] = "services.manage"
	m["GET /api/v1/admin/services/{id}/cdrom"] = "services.view"
	m["POST /api/v1/admin/services/{id}/cdrom"] = "services.manage"
	m["PUT /api/v1/admin/products/{id}/purchase-fields"] = "products.manage"
	m["POST /api/v1/admin/products/{id}/configuration-rules"] = "products.manage"
	m["PUT /api/v1/admin/products/{id}/configuration-rules/{rule_id}"] = "products.manage"
	m["DELETE /api/v1/admin/products/{id}/configuration-rules/{rule_id}"] = "products.manage"
	m["POST /api/v1/admin/invoices/{id}/payments/{payment_id}/reconcile"] = "payments.reconcile"
	m["GET /api/v1/admin/site-settings"] = "settings.view"
	m["PUT /api/v1/admin/site-settings"] = "settings.manage"
	m["GET /api/v1/admin/auth-settings"] = "settings.view"
	m["PUT /api/v1/admin/auth-settings"] = "settings.manage"
	m["GET /api/v1/admin/security/mfa"] = "settings.view"
	m["PUT /api/v1/admin/security/mfa"] = "settings.manage"
	m["GET /api/v1/admin/theme-settings"] = "settings.view"
	m["PUT /api/v1/admin/theme-settings"] = "settings.manage"
	m["GET /api/v1/admin/site-fields"] = "content.view"
	m["POST /api/v1/admin/site-fields"] = "content.manage"
	m["PATCH /api/v1/admin/site-fields/{id}"] = "content.manage"
	m["DELETE /api/v1/admin/site-fields/{id}"] = "content.manage"
	m["GET /api/v1/admin/navigation"] = "content.view"
	m["POST /api/v1/admin/navigation"] = "content.manage"
	m["PATCH /api/v1/admin/navigation/{id}"] = "content.manage"
	m["DELETE /api/v1/admin/navigation/{id}"] = "content.manage"
	m["GET /api/v1/admin/friendly-links"] = "content.view"
	m["POST /api/v1/admin/friendly-links"] = "content.manage"
	m["PATCH /api/v1/admin/friendly-links/{id}"] = "content.manage"
	m["DELETE /api/v1/admin/friendly-links/{id}"] = "content.manage"
	m["GET /api/v1/admin/file-groups"] = "downloads.view"
	m["POST /api/v1/admin/file-groups"] = "downloads.manage"
	m["PUT /api/v1/admin/file-groups/{id}"] = "downloads.manage"
	m["DELETE /api/v1/admin/file-groups/{id}"] = "downloads.manage"
	m["GET /api/v1/admin/certification-settings"] = "certifications.view"
	m["PUT /api/v1/admin/certification-settings"] = "certifications.manage"
	m["GET /api/v1/admin/system/runtime"] = "settings.view"
	m["GET /api/v1/admin/system/about"] = "settings.view"
	m["GET /api/v1/admin/system/database"] = "settings.view"
	m["GET /api/v1/admin/system/upgrade"] = "settings.view"
	m["GET /api/v1/admin/jobs"] = "jobs.view"
	m["GET /api/v1/admin/scheduler-settings"] = "@super"
	m["PUT /api/v1/admin/scheduler-settings"] = "@super"
	m["GET /api/v1/admin/jobs/queue"] = "jobs.view"
	m["GET /api/v1/admin/logs/jobs"] = "@super"
	m["GET /api/v1/admin/logs/jobs/trend"] = "@super"
	m["GET /api/v1/admin/logs/admin-login"] = "audit.view"
	m["POST /api/v1/admin/logs/cleanup"] = "@super"
	m["GET /api/v1/admin/blacklist"] = "security.view"
	m["POST /api/v1/admin/blacklist"] = "security.manage"
	m["PATCH /api/v1/admin/blacklist/{id}"] = "security.manage"
	m["DELETE /api/v1/admin/blacklist/{id}"] = "security.manage"
	m["GET /api/v1/admin/logs/sms"] = "notifications.view"
	m["GET /api/v1/admin/logs/inbox"] = "notifications.view"
	m["GET /api/v1/admin/logs/api"] = "audit.view"
	m["GET /api/v1/admin/logs/email"] = "notifications.view"
	m["GET /api/v1/admin/marketing/options"] = "notifications.view"
	m["POST /api/v1/admin/marketing/audience-count"] = "notifications.view"
	m["GET /api/v1/admin/marketing"] = "notifications.view"
	m["GET /api/v1/admin/marketing/{id}"] = "notifications.view"
	m["POST /api/v1/admin/marketing"] = "notifications.manage"
	m["PATCH /api/v1/admin/marketing/{id}"] = "notifications.manage"
	m["POST /api/v1/admin/marketing/{id}/send"] = "notifications.manage"
	m["GET /api/v1/admin/order-settings"] = "settings.view"
	m["PUT /api/v1/admin/order-settings"] = "settings.manage"
	m["GET /api/v1/admin/payment-settings"] = "@super"
	m["PUT /api/v1/admin/payment-settings"] = "@super"
	m["GET /api/v1/admin/credit-settings"] = "settings.view"
	m["PUT /api/v1/admin/credit-settings"] = "settings.manage"
	m["GET /api/v1/admin/credit"] = "credit.view"
	m["GET /api/v1/admin/credit/consumptions"] = "credit.view"
	m["GET /api/v1/admin/credit/repayments"] = "credit.view"
	m["POST /api/v1/admin/credit/consumptions/{id}/repay"] = "credit.manage"
	m["PUT /api/v1/admin/credit/{id}/freeze"] = "credit.manage"
	m["PUT /api/v1/admin/credit/{id}/limit"] = "credit.manage"
	m["GET /api/v1/admin/currencies"] = "settings.view"
	m["POST /api/v1/admin/currencies"] = "settings.manage"
	m["PATCH /api/v1/admin/currencies/{id}"] = "settings.manage"
	m["DELETE /api/v1/admin/currencies/{id}"] = "settings.manage"
	m["POST /api/v1/admin/currencies/{id}/default"] = "settings.manage"
	m["GET /api/v1/admin/finance-settings"] = "settings.view"
	m["PUT /api/v1/admin/finance-settings"] = "settings.manage"
	m["GET /api/v1/admin/invoice-settings"] = "settings.view"
	m["PUT /api/v1/admin/invoice-settings"] = "settings.manage"
	m["POST /api/v1/admin/invoice-shipping-methods"] = "settings.manage"
	m["PATCH /api/v1/admin/invoice-shipping-methods/{id}"] = "settings.manage"
	m["DELETE /api/v1/admin/invoice-shipping-methods/{id}"] = "settings.manage"
	m["GET /api/v1/admin/contract-settings"] = "settings.view"
	m["PUT /api/v1/admin/contract-settings"] = "settings.manage"
	m["GET /api/v1/admin/smtp-settings"] = "@super"
	m["PUT /api/v1/admin/smtp-settings"] = "@super"
	m["GET /api/v1/admin/sms-settings"] = "@super"
	m["PUT /api/v1/admin/sms-settings"] = "@super"
	m["POST /api/v1/admin/sms-settings/test"] = "@super"
	m["POST /api/v1/admin/smtp-settings/test"] = "@super"
	m["GET /api/v1/admin/channel-catalogue"] = "@super"
	m["GET /api/v1/admin/channel-instances"] = "@super"
	m["POST /api/v1/admin/channel-instances"] = "@super"
	m["PUT /api/v1/admin/channel-instances/{id}"] = "@super"
	m["DELETE /api/v1/admin/channel-instances/{id}"] = "@super"
	m["GET /api/v1/admin/customer-field-definitions"] = "customers.view"
	m["GET /api/v1/admin/sales-owners"] = "@super"
	m["PUT /api/v1/admin/customers/{id}/sales-owner"] = "@super"
	m["GET /api/v1/admin/sales-settings"] = "sales.view"
	m["PUT /api/v1/admin/sales-settings"] = "sales.manage"
	m["GET /api/v1/admin/sales-commission-groups"] = "sales.view"
	m["POST /api/v1/admin/sales-commission-groups"] = "sales.manage"
	m["PATCH /api/v1/admin/sales-commission-groups/{id}"] = "sales.manage"
	m["DELETE /api/v1/admin/sales-commission-groups/{id}"] = "sales.manage"
	m["GET /api/v1/admin/sales-commission-tiers"] = "sales.view"
	m["POST /api/v1/admin/sales-commission-tiers"] = "sales.manage"
	m["PATCH /api/v1/admin/sales-commission-tiers/{id}"] = "sales.manage"
	m["DELETE /api/v1/admin/sales-commission-tiers/{id}"] = "sales.manage"
	m["GET /api/v1/admin/sales-staff"] = "sales.view"
	m["PATCH /api/v1/admin/sales-staff/{id}"] = "sales.manage"
	m["GET /api/v1/admin/service-groups"] = "products.view"
	m["POST /api/v1/admin/service-groups"] = "products.manage"
	m["PUT /api/v1/admin/service-groups/{id}"] = "products.manage"
	m["DELETE /api/v1/admin/service-groups/{id}"] = "products.manage"
	m["PUT /api/v1/admin/products/{id}/service-group"] = "products.manage"
	m["GET /api/v1/admin/services/page"] = "services.view"
	m["GET /api/v1/admin/resources"] = "resources.view"
	m["GET /api/v1/admin/customers/page"] = "customers.view"
	m["GET /api/v1/admin/certifications/page"] = "certifications.view"
	m["GET /api/v1/admin/affiliate/accounts/page"] = "affiliates.view"
	m["GET /api/v1/admin/affiliate/attributions/page"] = "affiliates.view"
	m["GET /api/v1/admin/affiliate/withdrawals/page"] = "affiliates.view"
	m["PUT /api/v1/admin/products/{id}/configuration-upgrade-policy"] = "products.manage"
	m["GET /api/v1/admin/products/{id}/upgrade-targets"] = "products.view"
	m["PUT /api/v1/admin/products/{id}/upgrade-targets"] = "products.manage"
	m["GET /api/v1/admin/traffic-products"] = "products.view"
	m["POST /api/v1/admin/traffic-products"] = "products.manage"
	m["PATCH /api/v1/admin/traffic-products/{id}"] = "products.manage"
	m["DELETE /api/v1/admin/traffic-products/{id}"] = "products.manage"
	m["GET /api/v1/admin/traffic-orders"] = "orders.view"
	m["GET /api/v1/admin/orders/page"] = "orders.view"
	m["POST /api/v1/admin/orders/commission"] = "orders.view"
	m["GET /api/v1/admin/traffic-orders/{id}"] = "orders.view"
	m["POST /api/v1/admin/traffic-orders/{id}/cancel"] = "orders.manage"
	m["POST /api/v1/admin/traffic-orders/{id}/delivery-verification"] = "services.manage"
	m["POST /api/v1/admin/services/{id}/configuration-changes/{change_id}/retry"] = "services.manage"
	groups := map[string][]string{
		"customer_files.view":           {"GET /api/v1/admin/customer-files", "GET /api/v1/admin/customer-files/{id}", "GET /api/v1/admin/customer-files/{id}/file", "GET /api/v1/admin/customer-files/owners"},
		"customer_files.manage":         {"POST /api/v1/admin/customer-files", "PUT /api/v1/admin/customer-files/{id}", "DELETE /api/v1/admin/customer-files/{id}", "POST /api/v1/admin/customer-files/bulk-delete"},
		"downloads.view":                {"GET /api/v1/admin/downloads", "GET /api/v1/admin/downloads/{id}", "GET /api/v1/admin/download-categories", "GET /api/v1/admin/download-settings", "GET /api/v1/admin/download-products"},
		"downloads.manage":              {"POST /api/v1/admin/downloads", "PUT /api/v1/admin/downloads/{id}", "DELETE /api/v1/admin/downloads/{id}", "POST /api/v1/admin/download-categories", "PUT /api/v1/admin/download-categories/{id}", "DELETE /api/v1/admin/download-categories/{id}", "PUT /api/v1/admin/download-settings"},
		"dashboard.view":                {"GET /api/v1/admin/dashboard"},
		"migrations.view":               {"GET /api/v1/admin/migrations/runs", "GET /api/v1/admin/migrations/reviews", "GET /api/v1/admin/migrations/reviews/{entity}/{id}"},
		"@super":                        {"POST /api/v1/admin/migrations/reviews/{entity}/{id}", "POST /api/v1/admin/ticket-departments", "PATCH /api/v1/admin/ticket-departments/{id}", "DELETE /api/v1/admin/ticket-departments/{id}", "GET /api/v1/admin/permissions", "GET /api/v1/admin/roles", "POST /api/v1/admin/roles", "PATCH /api/v1/admin/roles/{id}", "DELETE /api/v1/admin/roles/{id}", "GET /api/v1/admin/staff", "POST /api/v1/admin/staff", "PATCH /api/v1/admin/staff/{id}"},
		"notifications.view":            {"GET /api/v1/admin/notification-templates/page", "GET /api/v1/admin/notification-templates", "GET /api/v1/admin/email-outbox", "GET /api/v1/admin/sms-outbox"},
		"notifications.manage":          {"POST /api/v1/admin/notification-templates", "PATCH /api/v1/admin/notification-templates/{id}", "POST /api/v1/admin/email-outbox/{id}/retry", "POST /api/v1/admin/sms-outbox/{id}/retry"},
		"affiliates.view":               {"GET /api/v1/admin/affiliate/accounts", "GET /api/v1/admin/affiliate/attributions", "GET /api/v1/admin/affiliate/withdrawals", "GET /api/v1/admin/affiliate/withdrawals/{id}"},
		"affiliates.withdrawals.review": {"POST /api/v1/admin/affiliate/withdrawals/{id}/review"},
		"affiliates.payout":             {"POST /api/v1/admin/affiliate/withdrawals/{id}/payout", "POST /api/v1/admin/affiliate/withdrawals/{id}/target"},
		"affiliates.manage":             {"POST /api/v1/admin/affiliate/accounts/{id}/status"},
		"reports.view":                  {"GET /api/v1/admin/reports/income", "GET /api/v1/admin/reports/sales", "GET /api/v1/admin/reports/tickets", "GET /api/v1/admin/reports/new-clients", "GET /api/v1/admin/reports/customer-ranking", "GET /api/v1/admin/reports/product-income"},
		"products.view":                 {"GET /api/v1/admin/products", "GET /api/v1/admin/products/page", "GET /api/v1/admin/products/{id}", "GET /api/v1/admin/catalog-groups/{kind}"},
		"products.manage":               {"POST /api/v1/admin/products", "PATCH /api/v1/admin/products/{id}", "POST /api/v1/admin/products/{id}/duplicate", "POST /api/v1/admin/products/{id}/pricing", "POST /api/v1/admin/products/{id}/option-groups", "PUT /api/v1/admin/products/{id}/option-groups/{group_id}", "DELETE /api/v1/admin/products/{id}/option-groups/{group_id}", "POST /api/v1/admin/products/{id}/option-groups/{group_id}/options", "PUT /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}", "DELETE /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}", "PUT /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}/price-tiers", "POST /api/v1/admin/catalog-groups/{kind}", "PUT /api/v1/admin/catalog-groups/{kind}/{id}", "DELETE /api/v1/admin/catalog-groups/{kind}/{id}"},
		"content.view":                  {"GET /api/v1/admin/content", "GET /api/v1/admin/content/{id}", "GET /api/v1/admin/content-categories", "GET /api/v1/admin/content-tags"},
		"content.manage":                {"POST /api/v1/admin/content", "PUT /api/v1/admin/content/{id}", "POST /api/v1/admin/content/{id}/images", "DELETE /api/v1/admin/content/{id}", "POST /api/v1/admin/content-categories", "PUT /api/v1/admin/content-categories/{id}", "DELETE /api/v1/admin/content-categories/{id}", "POST /api/v1/admin/content-tags", "PUT /api/v1/admin/content-tags/{id}", "DELETE /api/v1/admin/content-tags/{id}"},
		"customers.view":                {"GET /api/v1/admin/customers", "GET /api/v1/admin/customers/{id}", "GET /api/v1/admin/customers/{id}/contacts", "GET /api/v1/admin/customers/{id}/affiliate"},
		"customers.manage":              {"POST /api/v1/admin/customers", "POST /api/v1/admin/customers/{id}/password", "PATCH /api/v1/admin/customers/{id}", "POST /api/v1/admin/customers/{id}/impersonate", "POST /api/v1/admin/customers/{id}/contacts", "PATCH /api/v1/admin/customers/{id}/contacts/{contact_id}", "DELETE /api/v1/admin/customers/{id}/contacts/{contact_id}", "GET /api/v1/admin/customer-fields", "POST /api/v1/admin/customer-fields", "PATCH /api/v1/admin/customer-fields/{id}", "DELETE /api/v1/admin/customer-fields/{id}"},
		"customer_groups.view":          {"GET /api/v1/admin/customer-groups", "GET /api/v1/admin/customer-product-groups", "GET /api/v1/admin/customer-product-discounts"},
		"customer_groups.manage":        {"POST /api/v1/admin/customer-groups", "PATCH /api/v1/admin/customer-groups/{id}", "DELETE /api/v1/admin/customer-groups/{id}", "PUT /api/v1/admin/customers/{id}/group", "POST /api/v1/admin/customer-product-groups", "PATCH /api/v1/admin/customer-product-groups/{id}", "DELETE /api/v1/admin/customer-product-groups/{id}", "PUT /api/v1/admin/customer-product-discounts"},
		"customer_levels.view":          {"GET /api/v1/admin/customer-levels"},
		"customer_levels.manage":        {"POST /api/v1/admin/customer-levels", "PATCH /api/v1/admin/customer-levels/{id}", "DELETE /api/v1/admin/customer-levels/{id}"},
		"orders.view":                   {"GET /api/v1/admin/orders", "GET /api/v1/admin/orders/{id}", "GET /api/v1/admin/renewal-orders"},
		"orders.manage":                 {"GET /api/v1/admin/orders/create-options", "POST /api/v1/admin/orders/quote", "POST /api/v1/admin/orders", "POST /api/v1/admin/orders/{id}/invoice", "DELETE /api/v1/admin/orders/{id}", "POST /api/v1/admin/orders/bulk-review", "POST /api/v1/admin/orders/bulk-delete", "POST /api/v1/admin/orders/{id}/cancel", "POST /api/v1/admin/orders/{id}/review", "POST /api/v1/admin/orders/{id}/notes", "POST /api/v1/admin/orders/bulk-cancel"},
		"invoices.view":                 {"GET /api/v1/admin/invoices", "GET /api/v1/admin/invoices/page", "GET /api/v1/admin/invoices/{id}", "GET /api/v1/admin/customer-transactions"},
		"invoices.manage":               {"GET /api/v1/admin/invoice-customers", "POST /api/v1/admin/invoices", "PUT /api/v1/admin/invoices/{id}", "POST /api/v1/admin/invoices/{id}/cancel", "POST /api/v1/admin/invoices/merge"},
		"invoices.refund":               {"POST /api/v1/admin/invoices/{id}/refund"},
		"invoice_applications.manage":   {"POST /api/v1/admin/invoice-applications/{id}/review", "POST /api/v1/admin/invoice-applications/{id}/adjust", "POST /api/v1/admin/invoice-applications/{id}/document", "POST /api/v1/admin/invoice-applications/{id}/adjustments/{adjustment_id}/document"},
		"invoice_applications.view":     {"GET /api/v1/admin/invoice-applications", "GET /api/v1/admin/invoice-applications/{id}", "GET /api/v1/admin/invoice-applications/{id}/documents/{document_id}/file"},
		"balance.manage":                {"POST /api/v1/admin/customers/{id}/balance"},
		"promos.view":                   {"GET /api/v1/admin/promo-codes", "GET /api/v1/admin/promo-codes/page"},
		"promos.manage":                 {"POST /api/v1/admin/promo-codes", "PATCH /api/v1/admin/promo-codes/{id}"},
		"services.view":                 {"GET /api/v1/admin/services", "GET /api/v1/admin/services/{id}", "GET /api/v1/admin/cancellations"},
		"services.manage":               {"POST /api/v1/admin/services/{id}/actions", "POST /api/v1/admin/services/{id}/delivery", "POST /api/v1/admin/cancellations/{id}/decision"},
		"tickets.view":                  {"GET /api/v1/admin/ticket-departments", "GET /api/v1/admin/tickets", "GET /api/v1/admin/tickets/page", "GET /api/v1/admin/ticket-assignees", "GET /api/v1/admin/tickets/{id}/notes", "GET /api/v1/admin/ticket-statuses", "GET /api/v1/admin/ticket-rules"},
		"tickets.manage":                {"GET /api/v1/admin/tickets/create-options", "POST /api/v1/admin/tickets", "PATCH /api/v1/admin/tickets/{id}/assignment", "POST /api/v1/admin/tickets/{id}/receive", "POST /api/v1/admin/tickets/{id}/transfer", "POST /api/v1/admin/tickets/merge", "POST /api/v1/admin/tickets/{id}/notes", "DELETE /api/v1/admin/tickets/{id}/notes/{note_id}", "POST /api/v1/admin/ticket-statuses", "PATCH /api/v1/admin/ticket-statuses/{id}", "DELETE /api/v1/admin/ticket-statuses/{id}", "POST /api/v1/admin/ticket-rules", "PATCH /api/v1/admin/ticket-rules/{id}", "DELETE /api/v1/admin/ticket-rules/{id}"},
		"ticket_presets.view":           {"GET /api/v1/admin/ticket-reply-presets", "GET /api/v1/admin/ticket-reply-preset-categories"},
		"ticket_presets.manage":         {"POST /api/v1/admin/ticket-reply-presets", "PATCH /api/v1/admin/ticket-reply-presets/{id}", "DELETE /api/v1/admin/ticket-reply-presets/{id}", "POST /api/v1/admin/ticket-reply-preset-categories", "PATCH /api/v1/admin/ticket-reply-preset-categories/{id}", "DELETE /api/v1/admin/ticket-reply-preset-categories/{id}"},
		"audit.view":                    {"GET /api/v1/admin/audit", "GET /api/v1/admin/logs"},
		"certifications.view":           {"GET /api/v1/admin/certifications", "GET /api/v1/admin/certifications/{id}", "GET /api/v1/admin/certifications/{id}/history"},
		"certifications.manage":         {"POST /api/v1/admin/certifications/{id}/review"},
	}
	for permission, patterns := range groups {
		for _, p := range patterns {
			if _, ok := m[p]; ok {
				panic("duplicate admin permission route")
			}
			m[p] = permission
		}
	}
	return m
}()

func (id identity) can(permission string) bool {
	return (id.Role == "admin" || id.Role == "super_admin") && (id.Role == "super_admin" || slices.Contains(id.Permissions, permission))
}

func (s *Server) permissionCatalogue(w http.ResponseWriter, r *http.Request) {
	write(w, 200, store.AdminPermissions)
}
func (s *Server) adminRoles(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT r.id,r.name,r.description,r.active,r.permissions,r.version,r.created_at,r.updated_at,
 (SELECT count(*) FROM users u WHERE u.admin_role_id=r.id) AS member_count FROM admin_roles r ORDER BY r.id`)
}
func (s *Server) saveAdminRole(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.Method != "POST" {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.AdminRoleInput
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.SaveAdminRole(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]any{"id": result})
}
func (s *Server) deleteAdminRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.DeleteAdminRole(r.Context(), identityFrom(r).ID, id, in.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) adminStaff(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, `SELECT u.id,u.username,u.email,u.display_name,u.role,u.status,u.admin_role_id,r.name AS admin_role_name,
 r.active AS admin_role_active,u.admin_version,u.mfa_enabled,u.created_at,u.customer_scope,u.customer_ownerless,u.sales_enabled,u.updated_at,u.ticket_scope,u.ticket_unassigned,
 ARRAY(SELECT department_id FROM admin_ticket_departments WHERE user_id=u.id ORDER BY department_id) AS ticket_department_ids
	 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.role IN ('admin','super_admin')
	 AND ($3='' OR u.username ILIKE '%'||$3||'%' OR u.display_name ILIKE '%'||$3||'%') ORDER BY u.id LIMIT $1 OFFSET $2`, limit, offset, strings.TrimSpace(r.URL.Query().Get("q")))
}
func (s *Server) saveAdminStaff(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.Method != "POST" {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.StaffInput
	if !decode(w, r, &in) {
		return
	}
	result, err := s.db.SaveStaff(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]any{"id": result})
}
