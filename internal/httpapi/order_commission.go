package httpapi

import (
	"net/http"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type adminOrderCommissionItem = store.AdminOrderCommissionItem

// adminOrderCommission returns a current-rule preview for selected orders.
// It is deliberately read-only: commission configuration is not a settlement
// record, and the preview must never credit an employee or customer balance.
func (s *Server) adminOrderCommission(w http.ResponseWriter, r *http.Request) {
	actor := identityFrom(r)
	if !actor.can("sales.view") {
		writeError(w, http.StatusForbidden, "permission_denied", "没有查看销售提成的权限")
		return
	}
	var input struct {
		OrderIDs []int64 `json:"order_ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.OrderIDs) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_order_ids", "一次最多计算100个订单")
		return
	}
	seen := make(map[int64]struct{}, len(input.OrderIDs))
	for _, id := range input.OrderIDs {
		if id < 1 {
			writeError(w, http.StatusBadRequest, "invalid_order_ids", "订单编号无效")
			return
		}
		if _, ok := seen[id]; ok {
			writeError(w, http.StatusBadRequest, "invalid_order_ids", "订单编号不能重复")
			return
		}
		seen[id] = struct{}{}
	}
	if len(input.OrderIDs) == 0 {
		write(w, http.StatusOK, map[string]any{"items": []adminOrderCommissionItem{}, "total_commission_cents": int64(0)})
		return
	}

	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Now().In(zone)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
	items, err := s.db.AdminOrderCommission(r.Context(), input.OrderIDs, monthStart, now, actor.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(items) != len(input.OrderIDs) {
		writeError(w, http.StatusNotFound, "not_found", "订单不存在或无权查看")
		return
	}
	var total int64
	for _, item := range items {
		total += item.CommissionCents
	}
	write(w, http.StatusOK, map[string]any{"items": items, "total_commission_cents": total})
}
