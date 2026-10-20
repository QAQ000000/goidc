package httpapi

import (
	"net/http"
	"sort"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type salesSettlementInput = store.SalesSettlementInput
type salesSettlementItem = store.SalesSettlementItem

func salesSettlementConflict(code, msg string) error {
	return &store.Conflict{Code: code, Message: msg}
}
func salesSettlementRange(in *salesSettlementInput) (time.Time, time.Time, error) {
	zone := time.FixedZone("Asia/Shanghai", 28800)
	start, e := time.ParseInLocation("2006-01", in.Month, zone)
	if e != nil || start.Year() < 1970 || start.Format("2006-01") != in.Month || !start.AddDate(0, 1, 0).Before(time.Now()) || len(in.OrderIDs) < 1 || len(in.OrderIDs) > 100 {
		return start, start, salesSettlementConflict("invalid_settlement", "请选择已结束上海月份和1至100个首购订单")
	}
	sort.Slice(in.OrderIDs, func(i, j int) bool { return in.OrderIDs[i] < in.OrderIDs[j] })
	for i, id := range in.OrderIDs {
		if id < 1 || (i > 0 && id == in.OrderIDs[i-1]) {
			return start, start, salesSettlementConflict("invalid_order_ids", "订单编号无效或重复")
		}
	}
	return start, start.AddDate(0, 1, 0), nil
}
func (s *Server) salesSettlementPreview(w http.ResponseWriter, r *http.Request) {
	s.salesSettlementWrite(w, r, false)
}
func (s *Server) salesSettlementCreate(w http.ResponseWriter, r *http.Request) {
	s.salesSettlementWrite(w, r, true)
}
func (s *Server) salesSettlementWrite(w http.ResponseWriter, r *http.Request, create bool) {
	var in salesSettlementInput
	if !decode(w, r, &in) {
		return
	}
	start, end, e := salesSettlementRange(&in)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	key := ""
	if create {
		var ok bool
		key, ok = mutationKey(w, r, "", true)
		if !ok {
			return
		}
		if len(in.PreviewDigest) != 64 {
			writeError(w, 400, "invalid_preview_digest", "请先预览")
			return
		}
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 403, "permission_denied", "会话失效")
		return
	}
	out, err := s.db.WriteSalesSettlement(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), key, in, start, end, create)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, out)
}

func (s *Server) salesSettlements(w http.ResponseWriter, r *http.Request) {
	if !identityFrom(r).can("sales.view") {
		writeError(w, 403, "permission_denied", "没有销售权限")
		return
	}
	id := int64(0)
	if r.PathValue("id") != "" {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 403, "permission_denied", "会话失效")
		return
	}
	limit, offset := pagination(r)
	out, err := s.db.SalesSettlements(r.Context(), identityFrom(r).ID, id, tokenHash(cookie.Value), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, out)
}
