package store

import (
	"context"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
)

type monitoringBinding struct {
	Owner             int64
	Profile, Upstream string
}

func (s *Store) monitoringBinding(ctx context.Context, actorID, serviceID int64, admin bool) (monitoringBinding, error) {
	var binding monitoringBinding
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return binding, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT coalesce(u.status='active' AND (NOT $2::boolean OR u.role='super_admin' OR (u.role='admin' AND r.active AND 'services.view'=ANY(r.permissions))),false)
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, actorID, admin).Scan(&allowed); err != nil {
		return binding, err
	}
	if !allowed {
		return binding, conflict("permission_denied", "没有查看服务监控的权限")
	}
	if admin {
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
			return binding, err
		}
	}
	var mode, status string
	if err = tx.QueryRow(ctx, `SELECT s.user_id,s.provision_mode,s.status,coalesce(s.delivery_snapshot->>'profile_id',''),s.upstream_id
 FROM services s WHERE s.id=$1 AND ($2::boolean OR s.user_id=$3) FOR SHARE`, serviceID, admin, actorID).Scan(&binding.Owner, &mode, &status, &binding.Profile, &binding.Upstream); err != nil {
		return binding, err
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return binding, err
	}
	if mode != "nokvm" || (status != "active" && status != "suspended") || !provision.ValidResourceID(binding.Upstream) {
		return binding, conflict("monitoring_unavailable", "该服务当前不支持资源监控")
	}
	if s.Provisioners[binding.Profile] == nil {
		return binding, conflict("supplier_unavailable", "服务供应商暂不可用")
	}
	return binding, tx.Commit(ctx)
}

func (s *Store) ServiceMonitoring(ctx context.Context, actorID, serviceID int64, admin bool, period string) (provision.Monitoring, error) {
	var result provision.Monitoring
	durations := map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}
	duration, ok := durations[period]
	if !ok {
		return result, conflict("invalid_monitoring_range", "监控范围无效")
	}
	binding, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return result, err
	}
	end := time.Now().UTC()
	callCtx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	result, err = s.Provisioners[binding.Profile].Monitoring(callCtx, binding.Upstream, end.Add(-duration), end)
	if err != nil {
		return provision.Monitoring{}, conflict("supplier_monitoring_failed", "供应商监控数据暂不可用，请稍后重试")
	}
	// Ownership, scope, state and grants may change while the supplier is slow.
	current, err := s.monitoringBinding(ctx, actorID, serviceID, admin)
	if err != nil {
		return provision.Monitoring{}, err
	}
	if current != binding {
		return provision.Monitoring{}, conflict("monitoring_resource_changed", "服务资源已变更，请刷新后重试")
	}
	return result, nil
}
