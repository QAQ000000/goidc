package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type bulkOrderMutationInput struct {
	OrderIDs         []int64         `json:"order_ids"`
	ExpectedVersions map[int64]int64 `json:"expected_versions"`
	Decision         string          `json:"decision"`
	Note             string          `json:"note"`
	Reason           string          `json:"reason"`
}

func (s *Server) bulkReviewOrders(w http.ResponseWriter, r *http.Request) {
	s.bulkOrderMutation(w, r, false)
}
func (s *Server) bulkDeleteOrders(w http.ResponseWriter, r *http.Request) {
	s.bulkOrderMutation(w, r, true)
}
func (s *Server) bulkOrderMutation(w http.ResponseWriter, r *http.Request, deletion bool) {
	var in bulkOrderMutationInput
	if !decode(w, r, &in) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(in.OrderIDs) < 1 || len(in.OrderIDs) > 100 || len(key) < 8 || len(key) > 100 {
		writeError(w, 400, "invalid_bulk_order_operation", "订单数量或幂等键无效")
		return
	}
	seen := map[int64]bool{}
	for _, id := range in.OrderIDs {
		if id < 1 || seen[id] {
			writeError(w, 400, "invalid_bulk_order_operation", "订单编号无效或重复")
			return
		}
		seen[id] = true
	}
	action := "review"
	if deletion {
		action = "delete"
	}
	if err := s.db.BindOrderBatch(r.Context(), identityFrom(r).ID, action, key, in); err != nil {
		s.fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(in.OrderIDs))
	succeeded, failed := 0, 0
	for _, id := range in.OrderIDs {
		item := map[string]any{"order_id": id}
		var err error
		var result any
		itemKey := fmt.Sprintf("%s:%d", key, id)
		if deletion {
			result, err = s.db.DeleteOrder(r.Context(), identityFrom(r).ID, id, in.ExpectedVersions[id], in.Reason, itemKey)
		} else {
			result, err = s.db.ReviewOrder(r.Context(), identityFrom(r).ID, id, in.Decision, in.Note, itemKey, in.ExpectedVersions[id])
		}
		if err == nil {
			item["status"] = "succeeded"
			item["result"] = result
			succeeded++
		} else {
			var conflict *store.Conflict
			item["status"] = "failed"
			failed++
			switch {
			case errors.As(err, &conflict):
				item["code"] = conflict.Code
				item["error"] = conflict.Message
			case errors.Is(err, pgx.ErrNoRows):
				item["code"] = "not_found"
				item["error"] = "订单不存在或无权操作"
			default:
				s.fail(w, r, err)
				return
			}
		}
		items = append(items, item)
	}
	write(w, 200, map[string]any{"items": items, "succeeded": succeeded, "failed": failed})
}
