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
	if kind == "section" {
		s.list(w, r, `SELECT s.*,(SELECT count(*) FROM product_categories c WHERE c.section_id=s.id) AS member_count FROM product_sections s ORDER BY sort_order,id`)
	} else {
		s.list(w, r, `SELECT c.*,s.name AS section_name,coalesce(s.active,true) AS section_active,(SELECT count(*) FROM products p WHERE p.category_id=c.id) AS member_count FROM product_categories c LEFT JOIN product_sections s ON s.id=c.section_id ORDER BY c.sort_order,c.id`)
	}
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
	saved, err := s.db.SaveCatalogGroup(r.Context(), identityFrom(r).ID, id, kind, input)
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
	if err := s.db.DeleteCatalogGroup(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion, kind); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
