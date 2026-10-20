package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

type NotificationTemplatePatch struct {
	ExpectedVersion    *int64  `json:"expected_version"`
	Name               *string `json:"name"`
	Subject            *string `json:"subject"`
	Body               *string `json:"body"`
	ProviderTemplateID *string `json:"provider_template_id"`
	Active             *bool   `json:"active"`
}

func (s *Store) UpdateAccountNotificationTemplate(ctx context.Context, actor, id int64, input NotificationTemplatePatch) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result, err := updateAccountNotificationTemplate(ctx, tx, actor, id, input)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
func updateAccountNotificationTemplate(ctx context.Context, tx pgx.Tx, actor, id int64, input NotificationTemplatePatch) (map[string]any, error) {

	var err error
	if err = LockFileCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	var role string
	var roleID *int64
	if err = tx.QueryRow(ctx, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE", actor).Scan(&role, &roleID); err != nil {
		return nil, err
	}
	if role != "super_admin" {
		var allowed bool
		if roleID != nil {
			if err = tx.QueryRow(ctx, "SELECT active AND 'notifications.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed); err != nil {
				return nil, err
			}
		}
		if !allowed {
			return nil, conflict("permission_denied", "没有通知模板管理权限")
		}
	}
	var kind, channel string
	var version int64
	if err = tx.QueryRow(ctx, "SELECT kind,channel,version FROM notification_templates WHERE id=$1 FOR UPDATE", id).Scan(&kind, &channel, &version); err != nil {
		return nil, err
	}
	if version != *input.ExpectedVersion {
		return nil, conflict("notification_template_conflict", "模板已被其他管理员修改，请刷新后重新编辑")
	}
	if channel == "email" && input.Subject != nil && *input.Subject == "" {
		return nil, &AccountBusinessError{400, "invalid_notification_template", "邮件主题无效"}
	}
	if _, err = tx.Exec(ctx, `UPDATE notification_templates SET name=COALESCE($2,name),subject=COALESCE($3,subject),body=COALESCE($4,body),provider_template_id=COALESCE($5,provider_template_id),active=COALESCE($6,active),version=version+1,updated_by=$7,updated_at=now() WHERE id=$1`, id, input.Name, input.Subject, input.Body, input.ProviderTemplateID, input.Active, actor); err != nil {
		return nil, err
	}
	if err = Audit(ctx, tx, actor, "notification_template.updated", "notification_template", id, map[string]any{"kind": kind, "active": input.Active}); err != nil {
		return nil, err
	}

	return map[string]any{"id": id, "channel": channel, "kind": kind, "version": version + 1}, nil
}
