package store

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// An explicitly supplied customer filter must never degrade into a global query.
// Resolve real customer scope before inspecting any customer's log records.
func (s *Store) accountLogCustomer(ctx context.Context, filters url.Values, actor int64) (*int64, error) {
	return accountLogCustomerQuery(ctx, s.Pool, filters, actor)
}

func accountLogCustomerQuery(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, filters url.Values, actor int64) (*int64, error) {
	values, present := filters["customer_id"]
	if !present {
		return nil, nil
	}
	invalid := &AccountBusinessError{400, "invalid_customer", "客户编号必须是正整数"}
	if len(values) != 1 || values[0] == "" {
		return nil, invalid
	}
	for _, char := range values[0] {
		if char < '0' || char > '9' {
			return nil, invalid
		}
	}
	id, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || id < 1 {
		return nil, invalid
	}
	var allowed bool
	if err = q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$2 AND role='customer' AND admin_customer_allowed($1,id))", actor, id).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, &AccountBusinessError{404, "not_found", "客户不存在或无权查看"}
	}
	return &id, nil
}

// Channels, aliases and placeholders are server constants. An address is never
// evidence of customer ownership: contacts and different customers may share it.
func accountOutboxCustomerSQL(channel, customer string) string {
	relation := `EXISTS(SELECT 1 FROM notification_delivery_events owner WHERE owner.` + channel + `_outbox_id=j.id AND owner.user_id=` + customer + `)
 OR EXISTS(SELECT 1 FROM ticket_notification_deliveries owner JOIN ticket_notification_events event ON event.id=owner.event_id JOIN tickets ticket ON ticket.id=event.ticket_id
 WHERE owner.` + channel + `_outbox_id=j.id AND ticket.user_id=` + customer + `)`
	if channel == "email" {
		relation += ` OR EXISTS(SELECT 1 FROM tickets ticket WHERE ticket.id=j.ticket_id AND ticket.user_id=` + customer + `)`
	}
	return "(" + relation + ")"
}

func (s *Store) AdminAuditLogPage(ctx context.Context, filters url.Values, actor int64, limit, offset int) (map[string]any, error) {
	customer, err := s.accountLogCustomer(ctx, filters, actor)
	if err != nil {
		return nil, err
	}
	f := accountLogFilters{where: []string{"TRUE"}}
	if customer != nil {
		f.args = append(f.args, actor, *customer)
		f.where = append(f.where, "admin_customer_allowed($1,$2)", customerAuditEntitySQL("$2", "$1"))
	}
	if query := strings.TrimSpace(filters.Get("q")); query != "" {
		f.args = append(f.args, "%"+query+"%")
		n := len(f.args)
		f.where = append(f.where, fmt.Sprintf("(a.action ILIKE $%d OR a.entity_type ILIKE $%d OR a.entity_id ILIKE $%d OR u.display_name ILIKE $%d)", n, n, n, n))
	}
	if err := accountLogDateFilters(filters, &f, "a.created_at"); err != nil {
		return nil, &AccountBusinessError{400, "invalid_log_date", err.Error()}
	}
	return customerAccountReadPage(ctx, s.Pool, "SELECT a.id,a.created_at,a.action,a.entity_type,a.entity_id,a.actor_id,u.display_name AS actor_name FROM audit_events a LEFT JOIN account_identities u ON u.id=a.actor_id WHERE "+strings.Join(f.where, " AND "), "id DESC", f.args, limit, offset)
}

// Customer history follows the audited business object, never the operator. Only
// known entity types and their server-written ownership snapshots are accepted;
// unresolved/global records stay in the global audit view. Compare IDs as text
// so session hashes, deleted entities and malformed historical IDs cannot cast-fail.
func customerAuditEntitySQL(customer, actor string) string {
	parts := []string{`(a.entity_type IN ('user','customer','customer_credit_account') AND a.entity_id=` + customer + `::bigint::text)`}
	for _, entity := range []struct{ kind, table, owner string }{
		{"order", "orders", "user_id"}, {"invoice", "invoices", "user_id"},
		{"api_key", "api_keys", "user_id"}, {"traffic_order", "traffic_orders", "user_id"},
		{"external_refund", "external_refunds", "user_id"},
		{"invoice_profile", "invoice_profiles", "user_id"}, {"invoice_address", "invoice_addresses", "user_id"},
		{"invoice_application", "invoice_applications", "user_id"},
		{"certification", "certification_profiles", "user_id"}, {"certification_profile", "certification_profiles", "user_id"},
		{"certification_submission", "certification_submissions", "user_id"},
		{"contract", "contracts", "customer_id"}, {"affiliate_account", "affiliate_accounts", "user_id"},
		{"notification_target", "notification_targets", "user_id"},
	} {
		parts = append(parts, `(a.entity_type='`+entity.kind+`' AND EXISTS(SELECT 1 FROM `+entity.table+` owned WHERE owned.id::text=a.entity_id AND owned.`+entity.owner+`=`+customer+`))`)
	}
	// A transfer audit belongs to both parties. Other service records retain the
	// owner of their immutable ID interval instead of following today's owner.
	parts = append(parts, `(a.entity_type='service' AND EXISTS(SELECT 1 FROM services owned WHERE owned.id::text=a.entity_id AND
 ((a.action='service.ownership.transferred' AND EXISTS(SELECT 1 FROM service_ownership_transfers ownership
   WHERE ownership.service_id=owned.id AND ownership.id::text=a.payload->>'transfer_id'
   AND (ownership.source_customer_id=`+customer+` OR ownership.target_customer_id=`+customer+`))) OR
  (a.action<>'service.ownership.transferred' AND `+serviceOwnershipRecordCustomerSQL("owned.id", "a.id", "audit_floor_id")+`=`+customer+`))))`)
	for _, entity := range []struct{ kind, table string }{{"customer_contact", "customer_contacts"}, {"customer_file", "customer_files"}} {
		// Deletions and file transfers retain their exact customer snapshots.
		parts = append(parts, `(a.entity_type='`+entity.kind+`' AND (a.payload->>'customer_id'=`+customer+`::bigint::text OR `+
			`(a.payload->>'customer_id' IS NULL AND EXISTS(SELECT 1 FROM `+entity.table+` owned WHERE owned.id::text=a.entity_id AND owned.customer_id=`+customer+`))`+
			` OR (a.action='customer_file.updated' AND a.payload->>'previous_customer_id'=`+customer+`::bigint::text)))`)
	}
	parts = append(parts,
		`(a.entity_type='invoice_application_document' AND EXISTS(SELECT 1 FROM invoice_application_documents doc JOIN invoice_applications owned ON owned.id=doc.application_id WHERE doc.id::text=a.entity_id AND owned.user_id=`+customer+`))`,
		`(a.entity_type='affiliate_link' AND EXISTS(SELECT 1 FROM affiliate_links link JOIN affiliate_accounts owned ON owned.id=link.account_id WHERE link.id::text=a.entity_id AND owned.user_id=`+customer+`))`)
	permission := ticketAccountNotificationPermissionSQL(actor, "ticket.department_id", "tickets.view")
	parts = append(parts, `(a.entity_type='ticket' AND EXISTS(SELECT 1 FROM tickets ticket WHERE ticket.id::text=a.entity_id AND ticket.user_id=`+customer+` AND `+permission+`))`)
	for _, entity := range []struct{ kind, table string }{{"ticket_reply", "ticket_replies"}, {"ticket_note", "ticket_notes"}, {"ticket_attachment", "ticket_attachments"}} {
		parts = append(parts, `(a.entity_type='`+entity.kind+`' AND EXISTS(SELECT 1 FROM tickets ticket WHERE ticket.user_id=`+customer+` AND `+permission+
			` AND (EXISTS(SELECT 1 FROM `+entity.table+` owned WHERE owned.id::text=a.entity_id AND owned.ticket_id=ticket.id)`+
			` OR (a.action IN ('ticket.note.deleted','ticket.attachment.deleted') AND a.payload->>'ticket_id'=ticket.id::text))))`)
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}
