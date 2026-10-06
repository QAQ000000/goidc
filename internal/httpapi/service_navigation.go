package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (s *Server) adminServiceGroups(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, `SELECT g.*,(SELECT count(*) FROM products p WHERE p.service_group_id=g.id) AS product_count FROM service_navigation_groups g ORDER BY g.sort_order,g.id`)
}
func (s *Server) saveServiceGroup(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.ServiceGroupInput
	if !decode(w, r, &in) {
		return
	}
	saved, err := s.db.SaveServiceGroup(r.Context(), identityFrom(r).ID, id, in)
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
func (s *Server) deleteServiceGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64  `json:"expected_version"`
		TargetGroupID   *int64 `json:"target_group_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.DeleteServiceGroup(r.Context(), identityFrom(r).ID, id, in.ExpectedVersion, in.TargetGroupID); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) assignProductServiceGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		GroupID          *int64 `json:"group_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.db.AssignProductServiceGroup(r.Context(), identityFrom(r).ID, id, in.ExpectedRevision, in.GroupID); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]int64{"id": id})
}
func (s *Server) serviceNavigation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := identityFrom(r).ID
	tx, err := s.db.Pool.Begin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = store.CatalogReadLock(ctx, tx); err == nil {
		err = store.InitializeServiceNavigation(ctx, tx, user)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(ctx, `SELECT g.id,g.name,g.icon,g.sort_order,g.version,p.visible,p.version AS preference_version,
 (SELECT count(*) FROM services s JOIN products product ON product.id=s.product_id WHERE s.user_id=$1 AND product.service_group_id=g.id) AS service_count,
 (SELECT count(*) FROM services s JOIN products product ON product.id=s.product_id WHERE s.user_id=$1 AND product.service_group_id=g.id AND s.status='active') AS active_count
 FROM service_navigation_groups g JOIN service_navigation_preferences p ON p.group_id=g.id AND p.user_id=$1 ORDER BY g.sort_order,g.id`, user)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, 200, items)
}
func (s *Server) setServiceNavigation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Visible         *bool `json:"visible"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Visible == nil {
		writeError(w, 400, "invalid_visibility", "请指定导航显示状态")
		return
	}
	if err := s.db.SetServiceNavigation(r.Context(), identityFrom(r).ID, id, in.ExpectedVersion, *in.Visible); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"updated": true})
}
