package store

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ticketRecipient struct {
	kind                                                   string
	id, customer                                           int64
	email, phone                                           string
	emailVerified, phoneVerified, emailEnabled, smsEnabled bool
	preferenceVersion                                      int64
	active                                                 bool
}

// QueueTicketNotification records recipient decisions and outbox jobs in the caller's business transaction.
func (s *Store) QueueTicketNotification(ctx context.Context, tx pgx.Tx, ticketID, replyID, actorID int64, eventKind, publicOrigin string) error {
	if eventKind != "customer_created" && eventKind != "customer_replied" && eventKind != "staff_replied" {
		return fmt.Errorf("invalid ticket notification kind")
	}
	if (eventKind == "customer_created") != (replyID == 0) {
		return fmt.Errorf("invalid ticket notification reply")
	}
	var owner int64
	var number, subject, body, author, customerName, department string
	err := tx.QueryRow(ctx, `SELECT t.user_id,t.ticket_no,t.subject,CASE WHEN $2::bigint=0 THEN t.body ELSE r.body END,a.display_name,u.display_name,coalesce(d.name,'') FROM tickets t JOIN users u ON u.id=t.user_id JOIN users a ON a.id=$3 LEFT JOIN ticket_departments d ON d.id=t.department_id LEFT JOIN ticket_replies r ON r.id=$2 AND r.ticket_id=t.id AND r.author_id=$3 WHERE t.id=$1 AND ($2=0 OR r.id IS NOT NULL)`, ticketID, replyID, actorID).Scan(&owner, &number, &subject, &body, &author, &customerName, &department)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("ticket:%d:created", ticketID)
	if replyID != 0 {
		direction := "customer"
		if eventKind == "staff_replied" {
			direction = "staff"
		}
		key = fmt.Sprintf("ticket-reply:%d:%s", replyID, direction)
	}
	var eventID int64
	err = tx.QueryRow(ctx, `INSERT INTO ticket_notification_events(event_key,ticket_id,reply_id,actor_id,kind) VALUES($1,$2,NULLIF($3,0),$4,$5) ON CONFLICT(event_key) DO NOTHING RETURNING id`, key, ticketID, replyID, actorID, eventKind).Scan(&eventID)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	query := `SELECT 'user',u.id,$2::bigint,u.email,coalesce(u.phone,''),u.email_verified,u.phone_verified,coalesce(p.email_enabled,true),coalesce(p.sms_enabled,false),coalesce(p.version,0),u.status='active' FROM users u LEFT JOIN user_ticket_notification_preferences p ON p.user_id=u.id WHERE u.id=$2`
	if eventKind != "staff_replied" {
		query = `WITH legal AS (SELECT u.* FROM users u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id JOIN tickets t ON t.id=$1 WHERE u.id<>$3 AND u.status='active' AND admin_ticket_department_allowed(u.id,t.department_id) AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.manage'=ANY(ar.permissions)))) SELECT 'user',u.id,$2::bigint,u.email,coalesce(u.phone,''),u.email_verified,u.phone_verified,coalesce(p.email_enabled,true),coalesce(p.sms_enabled,false),coalesce(p.version,0),u.status='active' FROM legal u LEFT JOIN user_ticket_notification_preferences p ON p.user_id=u.id WHERE u.id=(SELECT assignee_id FROM tickets WHERE id=$1) OR NOT EXISTS(SELECT 1 FROM legal a JOIN tickets t ON t.assignee_id=a.id WHERE t.id=$1) ORDER BY u.id`
	}
	// Use all placeholders even for the owner-only query to keep pgx parameter typing explicit.
	if eventKind == "staff_replied" {
		query += ` AND $1::bigint>0 AND $3::bigint>0 AND u.id<>$3`
	}
	rows, err := tx.Query(ctx, query, ticketID, owner, actorID)
	if err != nil {
		return err
	}
	recipients := []ticketRecipient{}
	for rows.Next() {
		var r ticketRecipient
		if err = rows.Scan(&r.kind, &r.id, &r.customer, &r.email, &r.phone, &r.emailVerified, &r.phoneVerified, &r.emailEnabled, &r.smsEnabled, &r.preferenceVersion, &r.active); err != nil {
			rows.Close()
			return err
		}
		recipients = append(recipients, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if eventKind == "staff_replied" {
		rows, err = tx.Query(ctx, `SELECT id,email,support_emails,status='active' FROM customer_contacts WHERE customer_id=$1 ORDER BY id`, owner)
		if err != nil {
			return err
		}
		for rows.Next() {
			r := ticketRecipient{kind: "contact", customer: owner, emailVerified: true}
			if err = rows.Scan(&r.id, &r.email, &r.emailEnabled, &r.active); err != nil {
				rows.Close()
				return err
			}
			recipients = append(recipients, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	origin, parseErr := url.Parse(publicOrigin)
	ticketURL := ""
	if parseErr == nil && (origin.Scheme == "https" || origin.Scheme == "http") && origin.Host != "" && origin.User == nil {
		ticketURL = strings.TrimRight(origin.Scheme+"://"+origin.Host, "/") + fmt.Sprintf("/console/tickets/%d", ticketID)
		if eventKind != "staff_replied" {
			ticketURL = strings.Replace(ticketURL, "/console/tickets/", "/admin/tickets/", 1)
		}
	}
	replacer := strings.NewReplacer("{{ticket_no}}", number, "{{subject}}", subject, "{{body}}", body, "{{author_name}}", author, "{{customer_name}}", customerName, "{{department_name}}", department, "{{ticket_url}}", ticketURL)
	for _, r := range recipients {
		for _, channel := range []string{"email", "sms"} {
			if r.kind == "contact" && channel == "sms" {
				continue
			}
			if err = s.queueTicketRecipient(ctx, tx, eventID, r, channel, "ticket_"+eventKind, replacer); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) queueTicketRecipient(ctx context.Context, tx pgx.Tx, eventID int64, r ticketRecipient, channel, kind string, replacer *strings.Replacer) error {
	address, enabled, verified := r.email, r.emailEnabled, r.emailVerified
	if channel == "sms" {
		address, enabled, verified = r.phone, r.smsEnabled, r.phoneVerified
	}
	normalized := strings.ToLower(strings.TrimSpace(address))
	// Phone verification accepts canonical E.164 only. Do not invent country codes or
	// remove interior separators from a verified identity; invalid legacy values skip.
	reason := ""
	if !r.active {
		reason = "inactive_recipient"
	} else if !enabled {
		reason = "unsubscribed"
	} else if !verified {
		reason = "unverified_address"
	} else if normalized == "" {
		reason = "missing_address"
	}
	var subject, body, providerID, instance, implementation string
	var version, instanceVersion int64
	var active bool
	err := tx.QueryRow(ctx, `SELECT subject,body,provider_template_id,version,active FROM notification_templates WHERE channel=$1 AND kind=$2`, channel, kind).Scan(&subject, &body, &providerID, &version, &active)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if reason == "" {
		if err == pgx.ErrNoRows {
			reason = "missing_template"
		} else if !active {
			reason = "inactive_template"
		}
	}
	category := "mail"
	if channel == "sms" {
		category = "sms"
	}
	err = tx.QueryRow(ctx, `SELECT id,version,implementation_id FROM channel_instances WHERE category=$1 AND enabled ORDER BY sort_order,id LIMIT 1`, category).Scan(&instance, &instanceVersion, &implementation)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if reason == "" && err == pgx.ErrNoRows {
		reason = "missing_channel"
	}
	if reason == "" && channel == "sms" && implementation == "idcsmart" && strings.TrimSpace(providerID) == "" {
		reason = "missing_provider_template"
	}
	var duplicate *int64
	if reason == "" {
		var id int64
		err = tx.QueryRow(ctx, `SELECT id FROM ticket_notification_deliveries WHERE event_id=$1 AND channel=$2 AND normalized_address=$3 AND outcome='queued'`, eventID, channel, normalized).Scan(&id)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if err == nil {
			duplicate = &id
			reason = "duplicate_address"
		}
	}
	subject = replacer.Replace(subject)
	body = replacer.Replace(body)
	if reason == "" && channel == "sms" && (!ValidSMSRecipient(normalized) || len(body) > 4096) {
		reason = "invalid_sms_payload"
	}
	outcome := "queued"
	if reason != "" {
		outcome = "skipped"
	}
	var deliveryID int64
	err = tx.QueryRow(ctx, `INSERT INTO ticket_notification_deliveries(event_id,recipient_kind,recipient_id,user_id,contact_id,customer_id,channel,address,normalized_address,preference_enabled,preference_version,address_verified,template_kind,template_version,channel_instance_id,rendered_subject,rendered_body,outcome,reason,duplicate_of,channel_instance_version,provider_template_id) VALUES($1,$2,$3,CASE WHEN $2='user' THEN $3::bigint ELSE NULL END,CASE WHEN $2='contact' THEN $3::bigint ELSE NULL END,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,0),NULLIF($13,''),$14,$15,$16,$17,$18,NULLIF($19,0),$20) RETURNING id`, eventID, r.kind, r.id, r.customer, channel, address, normalized, enabled, r.preferenceVersion, verified, kind, version, instance, subject, body, outcome, reason, duplicate, instanceVersion, providerID).Scan(&deliveryID)
	if err != nil {
		return err
	}
	if reason != "" {
		return nil
	}
	var outboxID int64
	if channel == "email" {
		err = tx.QueryRow(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,expires_at,channel_instance_id) VALUES($1,$2,$3,$4,now()+interval '7 days',$5) RETURNING id`, normalized, subject, body, kind, instance).Scan(&outboxID)
	} else {
		outboxID, err = s.queueSMSWithTemplateID(ctx, tx, normalized, body, kind, "", time.Now().Add(7*24*time.Hour), providerID, instance)
	}
	if err != nil {
		return err
	}
	column := "email_outbox_id"
	if channel == "sms" {
		column = "sms_outbox_id"
	}
	_, err = tx.Exec(ctx, `UPDATE ticket_notification_deliveries SET `+column+`=$2 WHERE id=$1`, deliveryID, outboxID)
	return err
}
