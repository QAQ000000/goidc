package httpapi

import (
	"net/http"
	"strconv"

	"github.com/codex/zjmfgo/internal/store"
)

func catalogSectionFilter(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	raw := r.URL.Query().Get("section_id")
	if raw == "" {
		return nil, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_section", "一级分组筛选无效")
		return nil, false
	}
	return &id, true
}
func catalogGroupKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	kind := r.PathValue("kind")
	if store.CatalogGroupTable(kind) == "" {
		writeError(w, 400, "invalid_catalog_group", "分组类型无效")
		return "", false
	}
	return kind, true
}
func (s *Server) adminCatalogGroups(w http.ResponseWriter, r *http.Request) {
	kind, ok := catalogGroupKind(w, r)
	if !ok {
		return
	}
	groups, err := s.db.AdminCatalogGroups(r.Context(), identityFrom(r).ID, kind)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, groups)
}
func (s *Server) adminCatalogGroupsPage(w http.ResponseWriter, r *http.Request) {
	kind, ok := catalogGroupKind(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	page, err := s.db.AdminCatalogGroupsPage(r.Context(), identityFrom(r).ID, kind, r.URL.Query(), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, page)
}
func (s *Server) saveCatalogGroup(w http.ResponseWriter, r *http.Request) {
	kind, ok := catalogGroupKind(w, r)
	if !ok {
		return
	}
	var id int64
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var input store.CatalogGroupInput
	if !decode(w, r, &input) {
		return
	}
	saved, err := s.db.SaveCatalogGroup(requestFileContext(r), identityFrom(r).ID, id, kind, input, r.Header.Get("Idempotency-Key"))
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
func (s *Server) deleteCatalogGroup(w http.ResponseWriter, r *http.Request) {
	kind, ok := catalogGroupKind(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.db.DeleteCatalogGroup(requestFileContext(r), identityFrom(r).ID, id, input.ExpectedVersion, kind); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
