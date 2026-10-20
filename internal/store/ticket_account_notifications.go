package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

func ReplayTicketAccountMutation(ctx context.Context, tx pgx.Tx, actor int64, key, operation, hash string, ticketID int64) (json.RawMessage, bool, error) {
	if key == "" {
		return nil, false, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("ticket-mutation:%d:%s:%s", actor, operation, key)); err != nil {
		return nil, false, err
	}
	var saved string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,result FROM ticket_mutation_receipts WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3`, actor, operation, key).Scan(&saved, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if saved != hash {
		return nil, false, ticketAccountError(409, "idempotency_conflict", "相同请求键已用于不同内容")
	}
	if ticketID == 0 {
		var result struct {
			ID int64 `json:"id"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			return nil, false, err
		}
		var allowed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id WHERE t.id=$1 AND t.merged_ticket_id IS NULL AND t.user_id=$2 AND coalesce(d.customer_visible,true))`, result.ID, actor).Scan(&allowed); err != nil {
			return nil, false, err
		}
		if !allowed {
			return nil, false, ticketAccountError(404, "not_found", "工单不存在或无权查看")
		}
	}
	return json.RawMessage(raw), true, nil
}
func SaveTicketAccountReceipt(ctx context.Context, tx pgx.Tx, actor int64, key, operation, hash string, id int64, result any) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ticket_mutation_receipts(actor_id,operation,idempotency_key,ticket_id,request_hash,result) VALUES($1,$2,$3,$4,$5,$6)`, actor, operation, key, id, hash, raw)
	return err
}

type TicketNotificationPreferencesInput struct {
	Email   *bool `json:"email_enabled"`
	SMS     *bool `json:"sms_enabled"`
	Version int64 `json:"expected_version"`
}

func (s *Store) TicketAccountNotificationPreferences(ctx context.Context, actor int64, in *TicketNotificationPreferencesInput) (map[string]any, error) {
	var email, sms bool
	var version int64
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_ticket_notification_preferences(user_id) VALUES($1) ON CONFLICT DO NOTHING`, actor); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT email_enabled,sms_enabled,version FROM user_ticket_notification_preferences WHERE user_id=$1 FOR UPDATE`, actor).Scan(&email, &sms, &version); err != nil {
			return err
		}
		if in != nil {
			if in.Version != version {
				return ticketAccountError(409, "stale_preferences", "通知偏好已更新，请刷新后重试")
			}
			email, sms = *in.Email, *in.SMS
			version++
			if _, err := tx.Exec(ctx, `UPDATE user_ticket_notification_preferences SET email_enabled=$2,sms_enabled=$3,version=$4,updated_at=now() WHERE user_id=$1`, actor, email, sms, version); err != nil {
				return err
			}
			if err := Audit(ctx, tx, actor, "ticket.notification_preferences.updated", "user", actor, map[string]any{"email_enabled": email, "sms_enabled": sms, "version": version}); err != nil {
				return err
			}
		}
		return nil
	})
	return map[string]any{"email_enabled": email, "sms_enabled": sms, "version": version}, err
}
func (s *Store) TicketAccountNotifications(ctx context.Context, id, actor int64, admin bool, limit, offset int) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var staff bool
	err = tx.QueryRow(ctx, `SELECT ($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id WHERE t.id=$1 AND (($2::boolean AND admin_ticket_department_allowed($3,t.department_id)) OR (t.user_id=$3 AND coalesce(d.customer_visible,true)))`, id, admin, actor).Scan(&staff)
	if err != nil {
		return nil, err
	}
	where := ` FROM ticket_notification_deliveries d JOIN ticket_notification_events e ON e.id=d.event_id WHERE e.ticket_id=$1 AND ($2::boolean OR (d.recipient_kind='user' AND d.user_id=$3))`
	page, err := ticketAccountPage(ctx, tx, `SELECT d.id,e.event_key,e.kind AS event_kind,e.reply_id,d.channel,d.outcome,d.reason,d.created_at,CASE WHEN $2 THEN d.recipient_kind ELSE 'user' END AS recipient_kind,CASE WHEN $2 THEN d.address ELSE NULL END AS address,d.email_outbox_id,d.sms_outbox_id,coalesce((SELECT status FROM email_outbox WHERE id=d.email_outbox_id),(SELECT status FROM sms_outbox WHERE id=d.sms_outbox_id)) AS outbox_status`+where, "id DESC", []any{id, staff, actor}, limit, offset)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return page, nil
}

// SQL arguments are fixed server-owned column/placeholder names and permission constants.
func ticketAccountNotificationPermissionSQL(actor, department, permission string) string {
	return `EXISTS(SELECT 1 FROM administrators tu LEFT JOIN admin_roles tr ON tr.id=tu.admin_role_id WHERE tu.id=` + actor + ` AND tu.status='active' AND (tu.role='super_admin' OR (tu.role='admin' AND tr.active AND '` + permission + `'=ANY(tr.permissions))) AND admin_ticket_department_allowed(tu.id,` + department + `))`
}
func TicketNotificationOutboxPredicate(channel, id, actor, permission string) string {
	relation := `SELECT tt.department_id FROM ticket_notification_deliveries td JOIN ticket_notification_events te ON te.id=td.event_id JOIN tickets tt ON tt.id=te.ticket_id WHERE td.` + channel + `_outbox_id=` + id
	if channel == "email" {
		relation += ` UNION ALL SELECT tt.department_id FROM email_outbox direct_job JOIN tickets tt ON tt.id=direct_job.ticket_id WHERE direct_job.id=` + id
	}
	// Unlinked historical ticket messages are visible/retryable only by active superadmins.
	return `NOT EXISTS(SELECT 1 FROM (` + relation + `) scope WHERE NOT (` + ticketAccountNotificationPermissionSQL(actor, "scope.department_id", permission) + `)) AND (EXISTS(` + relation + `) OR NOT EXISTS(SELECT 1 FROM ` + channel + `_outbox orphan WHERE orphan.id=` + id + ` AND orphan.kind IN ('ticket_created','ticket_customer_created','ticket_customer_replied','ticket_staff_replied')) OR EXISTS(SELECT 1 FROM administrators su WHERE su.id=` + actor + ` AND su.status='active' AND su.role='super_admin'))`
}
