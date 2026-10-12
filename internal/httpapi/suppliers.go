package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

func (s *Server) supplierProviders(w http.ResponseWriter, r *http.Request) {
	write(w, 200, supplier.Providers())
}

func (s *Server) supplierAPISettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	settings, err := s.db.ResourceAPISettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	accounts, err := s.db.SupplierAccountSummaries(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"enabled": settings.Enabled, "require_certification": settings.RequireCertification,
		"require_verified_phone": settings.RequireVerifiedPhone, "version": settings.Version,
		"updated_at": settings.UpdatedAt, "providers": supplier.Definitions(),
		"accounts": accounts, "account_total": len(accounts),
	})
}

func (s *Server) updateSupplierAPISettings(w http.ResponseWriter, r *http.Request) {
	var in store.ResourceAPISettingsInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.UpdateResourceAPISettings(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) testSupplierConnection(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "请提供本次测试的幂等键")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.TestSupplierConnection(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), id, in.ExpectedVersion, key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) suppliers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.list(w, r, `SELECT a.id,a.name,a.provider,a.base_url,a.username,a.active,a.version,octet_length(a.credentials_encrypted)>0 AS credentials_configured,a.running_job_id AS blocked_job_id,(SELECT count(*) FROM supplier_product_bindings b WHERE b.supplier_id=a.id) AS product_count FROM supplier_accounts a ORDER BY a.id`)
}
func (s *Server) saveSupplier(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.SupplierInput
	if !decode(w, r, &in) {
		return
	}
	saved, err := s.db.SaveSupplier(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved})
}
func (s *Server) supplierCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := s.db.SupplierCatalog(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, items)
}
func (s *Server) supplierProduct(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	pid, err := strconv.ParseInt(r.PathValue("product_id"), 10, 64)
	if err != nil || pid < 1 {
		writeError(w, 400, "invalid_product", "上游商品编号无效")
		return
	}
	item, err := s.db.SupplierProduct(r.Context(), id, pid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}
func (s *Server) importSupplierProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.SupplierImportInput
	if !decode(w, r, &in) {
		return
	}
	pid, err := s.db.ImportSupplierProduct(r.Context(), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]int64{"id": pid})
}
func (s *Server) supplierBinding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	b, err := s.db.SupplierBinding(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, b)
}
func (s *Server) supplierVerification(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.SupplierVerificationInput
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.VerifySupplierJob(r.Context(), identityFrom(r).ID, id, in); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"verified": true})
}
