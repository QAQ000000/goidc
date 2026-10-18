package store

import (
	"context"
	"encoding/json"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

const supplierLifecycleManualMessage = "平台未发送此动作，请在上游完成处理后回读核验；确认前保留当前服务状态"

func isSupplierLifecycleAction(action string) bool {
	return action == "suspend" || action == "resume" || action == "terminate"
}

// Resolve the immutable service binding, never the product's current binding or
// credentials. The worker independently checks the configured runtime adapter.
func (s *Store) supplierServiceActionCapabilities(ctx context.Context, tx pgx.Tx, serviceID int64) (supplier.Capabilities, error) {
	var caps supplier.Capabilities
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT delivery_snapshot FROM services WHERE id=$1 AND provision_mode='supplier'", serviceID).Scan(&raw); err != nil {
		return caps, err
	}
	var snapshot supplierSnapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.BindingID < 1 || snapshot.SupplierID < 1 || snapshot.Provider == "" {
		return caps, conflict("supplier_snapshot_invalid", "供应商服务快照无效，请先核验绑定")
	}
	binding, err := s.supplierBindingWith(ctx, tx, snapshot.BindingID, false)
	if err != nil {
		return caps, err
	}
	if binding.SupplierID != snapshot.SupplierID || binding.Provider != snapshot.Provider {
		return caps, conflict("supplier_snapshot_invalid", "供应商服务绑定不匹配")
	}
	var provider string
	var active bool
	if err = tx.QueryRow(ctx, "SELECT provider,active FROM supplier_accounts WHERE id=$1", snapshot.SupplierID).Scan(&provider, &active); err != nil {
		return caps, err
	}
	if !active || provider != snapshot.Provider {
		return caps, conflict("supplier_unavailable", "供应商已停用或绑定不匹配")
	}
	for _, compiled := range supplier.Providers() {
		if compiled.ID == provider {
			return compiled.Capabilities, nil
		}
	}
	return caps, conflict("unsupported_provider", "当前运行程序未装配该供应商，请人工核验")
}
