package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) adminOrderQuote(w http.ResponseWriter, r *http.Request) {
	var in store.AdminOrderInput
	if !decodeLimit(w, r, &in, 256<<10) {
		return
	}
	out, err := s.db.QuoteAdminOrder(r.Context(), identityFrom(r).ID, in, s.paymentFallback())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) createAdminOrder(w http.ResponseWriter, r *http.Request) {
	var in store.AdminOrderInput
	if !decodeLimit(w, r, &in, 256<<10) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.CreateAdminOrder(r.Context(), identityFrom(r).ID, key, in, s.paymentFallback())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 201
	if out.Replayed {
		status = 200
	}
	write(w, status, out)
}
func (s *Server) createDeferredOrderInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.CreateDeferredOrderInvoice(r.Context(), identityFrom(r).ID, id, in.ExpectedVersion, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 201
	if out.Replayed {
		status = 200
	}
	write(w, status, out)
}
func (s *Server) adminOrderCreateOptions(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		recorder := &adminOrderOptionsRecorder{header: make(http.Header)}
		s.paymentProviders(recorder, r)
		if recorder.status >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(recorder.status)
			_, _ = w.Write(recorder.body)
			return
		}
		var providers []map[string]any
		if err := json.Unmarshal(recorder.body, &struct {
			Data *[]map[string]any `json:"data"`
		}{&providers}); err != nil {
			s.fail(w, r, err)
			return
		}
		var mailEnabled bool
		if err := s.db.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled)").Scan(&mailEnabled); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]any{"payment_providers": providers, "mail_enabled": mailEnabled})
		return
	}
	if kind != "customers" && kind != "products" {
		writeError(w, 400, "invalid_order_options", "订单配置类型无效")
		return
	}
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 120 {
		writeError(w, 400, "invalid_query", "搜索条件过长")
		return
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var total int64
	var rows pgx.Rows
	if kind == "customers" {
		where := `FROM users WHERE role='customer' AND status='active' AND admin_customer_allowed($1,id) AND ($2='' OR display_name ILIKE '%'||$2||'%' OR email ILIKE '%'||$2||'%' OR username ILIKE '%'||$2||'%')`
		if err = tx.QueryRow(r.Context(), "SELECT count(*) "+where, identityFrom(r).ID, q).Scan(&total); err == nil {
			rows, err = tx.Query(r.Context(), "SELECT id,display_name AS name,email,status "+where+" ORDER BY id DESC LIMIT $3 OFFSET $4", identityFrom(r).ID, q, limit, offset)
		}
	} else {
		productID := int64(0)
		if raw := r.URL.Query().Get("product_id"); raw != "" {
			productID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || productID < 1 {
				writeError(w, 400, "invalid_product", "商品标识无效")
				return
			}
		}
		where := `FROM products WHERE active AND catalog_category_visible(category_id) AND ($1='' OR name ILIKE '%'||$1||'%') AND ($2=0 OR id=$2)`
		if err = tx.QueryRow(r.Context(), "SELECT count(*) "+where, q, productID).Scan(&total); err == nil {
			rows, err = tx.Query(r.Context(), "SELECT "+productProjection(false)+" "+where+" ORDER BY sort_order,id LIMIT $3 OFFSET $4", q, productID, limit, offset)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	// Metadata may contain provider-side notes or opaque historical fields. The
	// create page receives typed purchase definitions, never arbitrary metadata.
	if kind == "products" {
		for _, item := range items {
			delete(item, "metadata")
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

// Share the existing provider listing contract without reproducing resolver
// branches in the order creation page. No headers/cookies are copied.
type adminOrderOptionsRecorder struct {
	header http.Header
	body   []byte
	status int
}

func (r *adminOrderOptionsRecorder) Header() http.Header    { return r.header }
func (r *adminOrderOptionsRecorder) WriteHeader(status int) { r.status = status }
func (r *adminOrderOptionsRecorder) Write(body []byte) (int, error) {
	r.body = append(r.body, body...)
	return len(body), nil
}
