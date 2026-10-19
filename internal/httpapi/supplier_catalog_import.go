package httpapi

import (
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) supplierImportPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.SupplierCatalogImportInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.PreviewSupplierCatalogImport(requestFileContext(r), identityFrom(r).ID, id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) supplierImportBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "请提供本次导入的幂等键")
		return
	}
	var in store.SupplierCatalogImportInput
	if !decode(w, r, &in) {
		return
	}
	out, err := s.db.ImportSupplierCatalog(requestFileContext(r), identityFrom(r).ID, id, key, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, out)
}
