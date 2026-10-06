package store

import (
	"context"
	"encoding/json"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

// The legacy catalogue is product-owned. A supplier change cannot reinterpret
// template IDs for an existing service; the purchase profile remains authoritative.
func serviceTemplates(ctx context.Context, tx pgx.Tx, serviceID int64) ([]provision.Template, error) {
	var raw []byte
	var profile, mode string
	if err := tx.QueryRow(ctx, `SELECT p.delivery_config,p.provision_mode,coalesce(s.delivery_snapshot->>'profile_id','')
 FROM services s JOIN products p ON p.id=s.product_id WHERE s.id=$1 FOR SHARE OF p`, serviceID).Scan(&raw, &mode, &profile); err != nil {
		return nil, err
	}
	var delivery provision.Delivery
	if mode != "nokvm" || json.Unmarshal(raw, &delivery) != nil || delivery.ProfileID != profile || delivery.Validate() != nil {
		return []provision.Template{}, nil
	}
	if delivery.Templates == nil {
		return []provision.Template{}, nil
	}
	return delivery.Templates, nil
}

func (s *Store) ServiceTemplates(ctx context.Context, actorID, serviceID int64, admin bool) ([]provision.Template, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if admin {
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
			return nil, err
		}
	}
	var mode string
	if err = tx.QueryRow(ctx, `SELECT provision_mode FROM services WHERE id=$1 AND ($2::boolean OR user_id=$3)`, serviceID, admin, actorID).Scan(&mode); err != nil {
		return nil, err
	}
	if mode != "nokvm" {
		return nil, conflict("unsupported_reinstall_mode", "该服务不支持系统重装")
	}
	templates, err := serviceTemplates(ctx, tx, serviceID)
	if err != nil {
		return nil, err
	}
	return templates, tx.Commit(ctx)
}

func pendingReinstallTemplate(ctx context.Context, tx pgx.Tx, job ProvisionJob) (provision.Template, error) {
	var raw []byte
	var template provision.Template
	if err := tx.QueryRow(ctx, `SELECT request_payload FROM provision_jobs WHERE id=$1 AND service_id=$2 AND action='reinstall'`, job.ID, job.ServiceID).Scan(&raw); err != nil {
		return template, err
	}
	if json.Unmarshal(raw, &template) != nil || template.Validate() != nil {
		return template, conflict("invalid_reinstall_request", "重装任务镜像配置无效")
	}
	return template, nil
}
