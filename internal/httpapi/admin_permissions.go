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
	m["GET /api/v1/admin/provision-profiles"] = "products.view"
	m["POST /api/v1/admin/services/{id}/delivery-verification"] = "services.manage"
	m["POST /api/v1/admin/services/{id}/credentials"] = "services.manage"
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
	m["GET /api/v1/admin/site-settings"] = "@super"
	m["PUT /api/v1/admin/site-settings"] = "@super"
	m["GET /api/v1/admin/sales-owners"] = "@super"
	m["PUT /api/v1/admin/customers/{id}/sales-owner"] = "@super"
	m["GET /api/v1/admin/service-groups"] = "products.view"
	m["POST /api/v1/admin/service-groups"] = "products.manage"
	m["PUT /api/v1/admin/service-groups/{id}"] = "products.manage"
	m["DELETE /api/v1/admin/service-groups/{id}"] = "products.manage"
	m["PUT /api/v1/admin/products/{id}/service-group"] = "products.manage"
	m["GET /api/v1/admin/services/page"] = "services.view"
	m["PUT /api/v1/admin/products/{id}/configuration-upgrade-policy"] = "products.manage"
	m["GET /api/v1/admin/products/{id}/upgrade-targets"] = "products.view"
	m["PUT /api/v1/admin/products/{id}/upgrade-targets"] = "products.manage"
	m["POST /api/v1/admin/services/{id}/configuration-changes/{change_id}/retry"] = "services.manage"
	groups := map[string][]string{
		"customer_files.view":         {"GET /api/v1/admin/customer-files", "GET /api/v1/admin/customer-files/{id}", "GET /api/v1/admin/customer-files/{id}/file", "GET /api/v1/admin/customer-files/owners"},
		"customer_files.manage":       {"POST /api/v1/admin/customer-files", "PUT /api/v1/admin/customer-files/{id}", "DELETE /api/v1/admin/customer-files/{id}", "POST /api/v1/admin/customer-files/bulk-delete"},
		"downloads.view":              {"GET /api/v1/admin/downloads", "GET /api/v1/admin/downloads/{id}", "GET /api/v1/admin/download-categories", "GET /api/v1/admin/download-settings", "GET /api/v1/admin/download-products"},
		"downloads.manage":            {"POST /api/v1/admin/downloads", "PUT /api/v1/admin/downloads/{id}", "DELETE /api/v1/admin/downloads/{id}", "POST /api/v1/admin/download-categories", "PUT /api/v1/admin/download-categories/{id}", "DELETE /api/v1/admin/download-categories/{id}", "PUT /api/v1/admin/download-settings"},
		"dashboard.view":              {"GET /api/v1/admin/dashboard"},
		"migrations.view":             {"GET /api/v1/admin/migrations/runs", "GET /api/v1/admin/migrations/reviews", "GET /api/v1/admin/migrations/reviews/{entity}/{id}"},
		"@super":                      {"POST /api/v1/admin/migrations/reviews/{entity}/{id}", "POST /api/v1/admin/ticket-departments", "PATCH /api/v1/admin/ticket-departments/{id}", "DELETE /api/v1/admin/ticket-departments/{id}", "GET /api/v1/admin/permissions", "GET /api/v1/admin/roles", "POST /api/v1/admin/roles", "PATCH /api/v1/admin/roles/{id}", "DELETE /api/v1/admin/roles/{id}", "GET /api/v1/admin/staff", "POST /api/v1/admin/staff", "PATCH /api/v1/admin/staff/{id}"},
		"notifications.view":          {"GET /api/v1/admin/notification-templates", "GET /api/v1/admin/email-outbox"},
		"notifications.manage":        {"POST /api/v1/admin/notification-templates", "PATCH /api/v1/admin/notification-templates/{id}", "POST /api/v1/admin/email-outbox/{id}/retry"},
		"reports.view":                {"GET /api/v1/admin/reports/income", "GET /api/v1/admin/reports/new-clients", "GET /api/v1/admin/reports/customer-ranking", "GET /api/v1/admin/reports/product-income"},
		"products.view":               {"GET /api/v1/admin/products", "GET /api/v1/admin/products/page", "GET /api/v1/admin/products/{id}", "GET /api/v1/admin/catalog-groups/{kind}"},
		"products.manage":             {"POST /api/v1/admin/products", "PATCH /api/v1/admin/products/{id}", "POST /api/v1/admin/products/{id}/duplicate", "POST /api/v1/admin/products/{id}/pricing", "POST /api/v1/admin/products/{id}/option-groups", "PUT /api/v1/admin/products/{id}/option-groups/{group_id}", "DELETE /api/v1/admin/products/{id}/option-groups/{group_id}", "POST /api/v1/admin/products/{id}/option-groups/{group_id}/options", "PUT /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}", "DELETE /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}", "PUT /api/v1/admin/products/{id}/option-groups/{group_id}/options/{option_id}/price-tiers", "POST /api/v1/admin/catalog-groups/{kind}", "PUT /api/v1/admin/catalog-groups/{kind}/{id}", "DELETE /api/v1/admin/catalog-groups/{kind}/{id}"},
		"content.view":                {"GET /api/v1/admin/content", "GET /api/v1/admin/content/{id}", "GET /api/v1/admin/content-categories", "GET /api/v1/admin/content-tags"},
		"content.manage":              {"POST /api/v1/admin/content", "PUT /api/v1/admin/content/{id}", "DELETE /api/v1/admin/content/{id}", "POST /api/v1/admin/content-categories", "PUT /api/v1/admin/content-categories/{id}", "DELETE /api/v1/admin/content-categories/{id}", "POST /api/v1/admin/content-tags", "PUT /api/v1/admin/content-tags/{id}", "DELETE /api/v1/admin/content-tags/{id}"},
		"customers.view":              {"GET /api/v1/admin/customers", "GET /api/v1/admin/customers/{id}/contacts"},
		"customers.manage":            {"POST /api/v1/admin/customers/{id}/password", "PATCH /api/v1/admin/customers/{id}", "POST /api/v1/admin/customers/{id}/contacts", "PATCH /api/v1/admin/customers/{id}/contacts/{contact_id}", "DELETE /api/v1/admin/customers/{id}/contacts/{contact_id}"},
		"customer_groups.view":        {"GET /api/v1/admin/customer-groups"},
		"customer_groups.manage":      {"POST /api/v1/admin/customer-groups", "PATCH /api/v1/admin/customer-groups/{id}", "DELETE /api/v1/admin/customer-groups/{id}", "PUT /api/v1/admin/customers/{id}/group"},
		"orders.view":                 {"GET /api/v1/admin/orders", "GET /api/v1/admin/orders/{id}"},
		"orders.manage":               {"POST /api/v1/admin/orders/{id}/cancel", "POST /api/v1/admin/orders/{id}/review", "POST /api/v1/admin/orders/{id}/notes", "POST /api/v1/admin/orders/bulk-cancel"},
		"invoices.view":               {"GET /api/v1/admin/invoices", "GET /api/v1/admin/invoices/page", "GET /api/v1/admin/invoices/{id}"},
		"invoices.manage":             {"GET /api/v1/admin/invoice-customers", "POST /api/v1/admin/invoices", "PUT /api/v1/admin/invoices/{id}", "POST /api/v1/admin/invoices/{id}/cancel", "POST /api/v1/admin/invoices/merge"},
		"invoices.refund":             {"POST /api/v1/admin/invoices/{id}/refund"},
		"invoice_applications.view":   {"GET /api/v1/admin/invoice-applications", "GET /api/v1/admin/invoice-applications/{id}"},
		"invoice_applications.manage": {"POST /api/v1/admin/invoice-applications/{id}/review"},
		"balance.manage":              {"POST /api/v1/admin/customers/{id}/balance"},
		"promos.view":                 {"GET /api/v1/admin/promo-codes"},
		"promos.manage":               {"POST /api/v1/admin/promo-codes", "PATCH /api/v1/admin/promo-codes/{id}"},
		"services.view":               {"GET /api/v1/admin/services", "GET /api/v1/admin/services/{id}", "GET /api/v1/admin/cancellations"},
		"services.manage":             {"POST /api/v1/admin/services/{id}/actions", "POST /api/v1/admin/services/{id}/delivery", "POST /api/v1/admin/cancellations/{id}/decision"},
		"tickets.view":                {"GET /api/v1/admin/ticket-departments", "GET /api/v1/admin/tickets", "GET /api/v1/admin/ticket-assignees", "GET /api/v1/admin/tickets/{id}/notes"},
		"tickets.manage":              {"PATCH /api/v1/admin/tickets/{id}/assignment", "POST /api/v1/admin/tickets/{id}/receive", "POST /api/v1/admin/tickets/{id}/transfer", "POST /api/v1/admin/tickets/merge", "POST /api/v1/admin/tickets/{id}/notes", "DELETE /api/v1/admin/tickets/{id}/notes/{note_id}"},
		"ticket_presets.view":         {"GET /api/v1/admin/ticket-reply-presets", "GET /api/v1/admin/ticket-reply-preset-categories"},
		"ticket_presets.manage":       {"POST /api/v1/admin/ticket-reply-presets", "PATCH /api/v1/admin/ticket-reply-presets/{id}", "DELETE /api/v1/admin/ticket-reply-presets/{id}", "POST /api/v1/admin/ticket-reply-preset-categories", "PATCH /api/v1/admin/ticket-reply-preset-categories/{id}", "DELETE /api/v1/admin/ticket-reply-preset-categories/{id}"},
		"audit.view":                  {"GET /api/v1/admin/audit"},
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
