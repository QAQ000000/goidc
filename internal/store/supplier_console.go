package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

type supplierConsoleBinding struct {
	SupplierID, UpstreamID, ProductID, Version, ServiceVersion int64
	ActorRole                                                  string
	Snapshot                                                   string
}
type supplierTemplateListing struct {
	binding   supplierConsoleBinding
	templates []provision.Template
}
type supplierReinstallKey struct{}

func (s *Store) supplierConsoleBindingTx(ctx context.Context, tx pgx.Tx, actor, serviceID int64, admin, manage bool) (supplierConsoleBinding, error) {
	var out supplierConsoleBinding
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	var actorActive bool
	if err := tx.QueryRow(ctx, "SELECT status='active',role FROM users WHERE id=$1 FOR SHARE", actor).Scan(&actorActive, &out.ActorRole); err != nil {
		return out, err
	}
	if !actorActive || (!admin && out.ActorRole != "customer") {
		return out, conflict("permission_denied", "账户状态或角色无权执行此操作")
	}
	if admin {
		if manage {
			if err := requireDeliveryManage(ctx, tx, actor); err != nil {
				return out, err
			}
		}
		if err := LockAdminResourceCustomer(ctx, tx, actor, "service", serviceID); err != nil {
			return out, err
		}
	}
	var upstream string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT upstream_id,delivery_snapshot,version FROM services WHERE id=$1 AND provision_mode='supplier' AND status='active' AND ($2::boolean OR user_id=$3) FOR SHARE`, serviceID, admin, actor).Scan(&upstream, &raw, &out.ServiceVersion); err != nil {
		return out, err
	}
	var snap supplierSnapshot
	if json.Unmarshal(raw, &snap) != nil || snap.SupplierID < 1 {
		return out, conflict("supplier_snapshot_invalid", "供应商绑定无效")
	}
	out.UpstreamID, _ = strconv.ParseInt(upstream, 10, 64)
	if out.UpstreamID < 1 {
		return out, conflict("supplier_resource_missing", "上游资源标识缺失")
	}
	out.SupplierID, out.Snapshot = snap.SupplierID, string(raw)
	var active bool
	if err := tx.QueryRow(ctx, `SELECT a.version,a.active,b.upstream_product_id FROM supplier_accounts a JOIN supplier_product_bindings b ON b.supplier_id=a.id WHERE a.id=$1 AND b.id=$2 FOR SHARE OF a,b`, snap.SupplierID, snap.BindingID).Scan(&out.Version, &active, &out.ProductID); err != nil {
		return out, err
	}
	if !active {
		return out, conflict("supplier_unavailable", "供应商已停用")
	}
	return out, nil
}

func (s *Store) supplierConsoleBinding(ctx context.Context, actor, serviceID int64, admin, manage bool) (supplierConsoleBinding, error) {
	return withTx(ctx, s, func(tx pgx.Tx) (supplierConsoleBinding, error) {
		var err error
		out, err := s.supplierConsoleBindingTx(ctx, tx, actor, serviceID, admin, manage)
		if err != nil {
			return out, err
		}
		return out, nil
	})
}

func (s *Store) supplierTemplates(ctx context.Context, actor, serviceID int64, admin bool) (supplierTemplateListing, error) {
	var out supplierTemplateListing
	b, err := s.supplierConsoleBinding(ctx, actor, serviceID, admin, false)
	if err != nil {
		return out, err
	}
	a, _, err := s.supplierAdapter(ctx, b.SupplierID)
	if err != nil {
		return out, err
	}
	console, ok := a.(supplier.ConsoleAdapter)
	if !ok || !a.Capabilities().Reinstall {
		return out, conflict("unsupported_reinstall_mode", "供应商不支持重装")
	}
	items, err := console.Templates(ctx, b.UpstreamID)
	if err != nil {
		return out, supplierReadError(err)
	}
	after, err := s.supplierConsoleBinding(ctx, actor, serviceID, admin, false)
	if err != nil {
		return out, err
	}
	if after != b {
		return out, conflict("supplier_resource_changed", "服务资源已变更，请刷新")
	}
	out.binding = b
	out.templates = []provision.Template{}
	for _, t := range items {
		login := "root"
		if strings.Contains(strings.ToLower(t.Name+" "+t.Group), "windows") {
			login = "administrator"
		}
		out.templates = append(out.templates, provision.Template{ID: t.ID, Name: t.Name, Group: t.Group, Login: login})
	}
	return out, nil
}

func (s *Store) ServiceVNC(ctx context.Context, actor, serviceID int64, admin bool) (string, error) {
	b, err := s.supplierConsoleBinding(ctx, actor, serviceID, admin, true)
	if err != nil {
		return "", err
	}
	a, _, err := s.supplierAdapter(ctx, b.SupplierID)
	if err != nil {
		return "", err
	}
	console, ok := a.(supplier.ConsoleAdapter)
	if !ok || !a.Capabilities().VNC {
		return "", conflict("unsupported_console_mode", "供应商不支持控制台")
	}
	u, err := console.VNC(ctx, b.UpstreamID)
	if err != nil {
		return "", supplierReadError(err)
	}
	return withTx(ctx, s, func(tx pgx.Tx) (string, error) {
		var err error
		after, err := s.supplierConsoleBindingTx(ctx, tx, actor, serviceID, admin, true)
		if err != nil {
			return "", err
		}
		if after != b {
			return "", conflict("supplier_resource_changed", "服务资源已变更，请刷新")
		}
		if !supplier.ValidConsoleURL(u) {
			return "", conflict("console_url_invalid", "供应商控制台地址无效")
		}
		if err = Audit(ctx, tx, actor, "service.console.opened", "service", serviceID, map[string]any{}); err != nil {
			return "", err
		}
		return u, nil
	})
}

// ServiceSupplierCapabilitiesTx is called only after the service visibility check.
// It reads the immutable service binding, never the current product binding or a remote API.
func (s *Store) ServiceSupplierCapabilitiesTx(ctx context.Context, tx pgx.Tx, serviceID int64, admin bool) (supplier.Capabilities, error) {
	var provider string
	var caps supplier.Capabilities
	err := tx.QueryRow(ctx, `SELECT a.provider FROM services s JOIN supplier_accounts a ON a.id::text=s.delivery_snapshot->>'supplier_id' JOIN supplier_product_bindings b ON b.id::text=s.delivery_snapshot->>'binding_id' AND b.supplier_id=a.id WHERE s.id=$1 AND s.provision_mode='supplier' AND a.active`, serviceID).Scan(&provider)
	if err == pgx.ErrNoRows {
		return caps, nil
	}
	if err != nil {
		return caps, err
	}
	// Capability metadata is public compiled information, independent of secrets.
	for _, compiled := range supplier.Providers() {
		if compiled.ID == provider {
			caps = compiled.Capabilities
			break
		}
	}
	if !admin {
		caps.Create, caps.Sync = false, false
		caps.Suspend, caps.Resume, caps.Terminate = false, false, false
	}
	return caps, nil
}
