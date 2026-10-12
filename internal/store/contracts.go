package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"regexp"
	"strings"
)

var contractPlaceholder = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

func RenderContract(content string, values map[string]string) (string, error) {
	if !contentTextValid(content, 100000, true) {
		return "", conflict("invalid_contract_template", "合同模板内容无效")
	}
	for _, m := range contractPlaceholder.FindAllStringSubmatch(content, -1) {
		if _, ok := values[m[1]]; !ok {
			return "", conflict("unknown_placeholder", "合同模板含未知占位符")
		}
	}
	result := contractPlaceholder.ReplaceAllStringFunc(content, func(m string) string { return values[m[2:len(m)-2]] })
	if strings.Contains(result, "{{") || strings.Contains(result, "}}") {
		return "", conflict("unknown_placeholder", "合同模板含无效占位符")
	}
	return result, nil
}
func ContractValues() map[string]string {
	return map[string]string{"customer_name": "", "customer_email": "", "product_name": "", "service_name": "", "domain": "", "paid_at": ""}
}
func (s *Store) ContractTx(ctx context.Context, actor int64, admin, manage, templates bool) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)")
	if err == nil {
		err = lockStorageCredential(ctx, tx, actor)
	}
	if err == nil {
		var locked int64
		err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 FOR SHARE", actor).Scan(&locked)
	}
	if err == nil && admin {
		_, err = tx.Exec(ctx, "SELECT id FROM admin_roles WHERE id=(SELECT admin_role_id FROM users WHERE id=$1) FOR SHARE", actor)
	}
	if err == nil {
		var allowed bool
		if admin {
			view, write := "contracts.view", "contracts.manage"
			if templates {
				view, write = "settings.view", "settings.manage"
			}
			permission := view
			if manage {
				permission = write
			}
			err = tx.QueryRow(ctx, `SELECT coalesce(u.status='active' AND (u.role='super_admin' OR(u.role='admin' AND r.active AND $2=ANY(r.permissions) AND $3=ANY(r.permissions))),false) FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, actor, view, permission).Scan(&allowed)
		} else {
			err = tx.QueryRow(ctx, "SELECT role='customer' AND status='active' FROM users WHERE id=$1", actor).Scan(&allowed)
		}
		if err == nil && !allowed {
			err = conflict("permission_denied", "没有合同权限")
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func ContractEvent(ctx context.Context, tx pgx.Tx, id, actor, version int64, action, before, after, note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO contract_history(contract_id,actor_id,action,before_status,after_status,note,version) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, actor, action, before, after, note, version)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO contract_documents(contract_id,version,content,signer_name,status) SELECT id,version,content,signer_name,status FROM contracts WHERE id=$1`, id)
	if err != nil {
		return err
	}
	return Audit(ctx, tx, actor, "contract."+action, "contract", id, map[string]any{"before_status": before, "after_status": after, "version": version})
}
