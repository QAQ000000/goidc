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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	tx, err := s.db.CatalogTx(r.Context(), actor)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Lock current user/role/session after the catalog lock, before replay or mutation.
	var role string
	var roleID *int64
	if err = tx.QueryRow(r.Context(), "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actor).Scan(&role, &roleID); err != nil {
		writeError(w, 403, "permission_denied", "权限已失效")
		return
	}
	if role != "super_admin" {
		var allowed bool
		if role != "admin" || roleID == nil {
			writeError(w, 403, "permission_denied", "没有管理商品权限")
			return
		}
		if err = tx.QueryRow(r.Context(), "SELECT active AND 'products.view'=ANY(permissions) AND 'products.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed); err != nil || !allowed {
			writeError(w, 403, "permission_denied", "权限已失效")
			return
		}
	}
	cookie, cookieErr := r.Cookie("zjmfgo_session")
	if cookieErr != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	var sessionUser int64
	if err = tx.QueryRow(r.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", tokenHash(cookie.Value), actor).Scan(&sessionUser); err != nil {
		writeError(w, 401, "unauthorized", "登录已失效")
		return
	}
	hash := settingMutationHash(struct {
		ID    int64
		Input store.GlobalConfigurationInput
	}{id, in})
	op := "global_configuration." + operation
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, op, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, replay)
		return
	}
	result, err := store.MutateGlobalConfiguration(r.Context(), tx, actor, id, operation, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, op, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, result)
}
