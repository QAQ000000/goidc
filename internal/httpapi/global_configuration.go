package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) globalConfigurationGroups(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	product := r.URL.Query().Get("product_id")
	if product != "" {
		if n, err := strconv.ParseInt(product, 10, 64); err != nil || n < 1 {
			writeError(w, 400, "invalid_id", "商品编号无效")
			return
		}
	}
	where := `($1='' OR g.name ILIKE '%'||$1||'%' OR g.description ILIKE '%'||$1||'%') AND ($2='' OR EXISTS(SELECT 1 FROM global_configuration_assignments a WHERE a.global_group_id=g.id AND a.product_id=NULLIF($2,'')::bigint))`
	s.writeListPage(w, r, "SELECT count(*) FROM global_configuration_groups g WHERE "+where,
		`SELECT g.*,COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'name',p.name,'revision',p.revision) ORDER BY p.id) FROM global_configuration_assignments a JOIN products p ON p.id=a.product_id WHERE a.global_group_id=g.id),'[]'::jsonb) AS products FROM global_configuration_groups g WHERE `+where+` ORDER BY g.id DESC`, []any{q, product}, limit, offset)
}
func (s *Server) globalConfigurationDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `SELECT g.*,COALESCE((SELECT jsonb_agg(jsonb_build_object('id',p.id,'name',p.name,'revision',p.revision) ORDER BY p.id) FROM global_configuration_assignments a JOIN products p ON p.id=a.product_id WHERE a.global_group_id=g.id),'[]'::jsonb) AS products FROM global_configuration_groups g WHERE g.id=$1`, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}
func (s *Server) mutateGlobalConfiguration(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.PathValue("id") != "" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	operation := "save"
	if strings.HasSuffix(r.URL.Path, "/copy") {
		operation = "copy"
	}
	if strings.HasSuffix(r.URL.Path, "/assignments") {
		operation = "assign"
	}
	if r.Method == "DELETE" {
		operation = "delete"
	}
	var in store.GlobalConfigurationInput
	if !decodeLimit(w, r, &in, 1<<20) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.MutateAccountGlobalConfiguration(requestFileContext(r), identityFrom(r).ID, id, operation, key, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, result)
}
