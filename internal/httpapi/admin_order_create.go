package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
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
		mailEnabled, err := s.db.AdminOrderMailEnabled(r.Context())
		if err != nil {
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
	var result map[string]any
	var err error
	if kind == "customers" {
		result, err = s.db.AdminCustomerCreateOptions(r.Context(), identityFrom(r).ID, r.URL.Query(), q, "order", limit, offset)
	} else {
		result, err = s.db.AdminOrderProductOptions(r.Context(), r.URL.Query(), q, limit, offset)
	}
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
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
