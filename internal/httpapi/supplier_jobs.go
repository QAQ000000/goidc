package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const supplierJobRows = `FROM provision_jobs j
JOIN services s ON s.id=j.service_id AND s.provision_mode='supplier'
JOIN users u ON u.id=s.user_id
JOIN products p ON p.id=s.product_id
JOIN supplier_accounts a ON s.delivery_snapshot->>'supplier_id'=a.id::text
WHERE admin_customer_allowed($1,u.id)`

func (s *Server) supplierJobs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	limit, offset, valid := serviceActivityPagination(r)
	if !valid {
		writeError(w, 400, "invalid_pagination", "分页参数无效")
		return
	}
	query := r.URL.Query()
	args := []any{identityFrom(r).ID}
	where := []string{}
	for _, spec := range []struct{ key, column string }{
		{"customer_id", "u.id"}, {"supplier_id", "a.id"},
	} {
		if value := query.Get(spec.key); value != "" {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id < 1 {
				writeError(w, 400, "invalid_filter", "筛选编号无效")
				return
			}
			args = append(args, id)
			where = append(where, fmt.Sprintf("%s=$%d", spec.column, len(args)))
		}
	}
	if value := query.Get("action"); value != "" {
		if len(value) > 50 || !validSupplierJobAction(value) {
			writeError(w, 400, "invalid_action", "动作筛选无效")
			return
		}
		args = append(args, value)
		where = append(where, fmt.Sprintf("j.action=$%d", len(args)))
	}
	if value := query.Get("status"); value != "" {
		if !validJobStatus(value) {
			writeError(w, 400, "invalid_job_status", "任务状态无效")
			return
		}
		args = append(args, value)
		where = append(where, fmt.Sprintf("j.status=$%d", len(args)))
	}
	if value := strings.TrimSpace(query.Get("q")); value != "" {
		if len(value) > 100 {
			writeError(w, 400, "invalid_search", "关键词过长")
			return
		}
		args = append(args, "%"+value+"%")
		where = append(where, fmt.Sprintf("(u.display_name ILIKE $%d OR u.username ILIKE $%d OR p.name ILIKE $%d OR a.name ILIKE $%d OR j.action ILIKE $%d OR s.name ILIKE $%d)", len(args), len(args), len(args), len(args), len(args), len(args)))
	}
	for _, spec := range []struct{ key, op string }{{"from", ">="}, {"to", "<"}} {
		if value := query.Get(spec.key); value != "" {
			day, err := time.Parse("2006-01-02", value)
			if err != nil {
				writeError(w, 400, "invalid_job_date", "日期无效")
				return
			}
			if spec.key == "to" {
				day = day.AddDate(0, 0, 1)
			}
			args = append(args, day)
			where = append(where, fmt.Sprintf("j.updated_at%s$%d", spec.op, len(args)))
		}
	}
	filter := ""
	if len(where) > 0 {
		filter = " AND " + strings.Join(where, " AND ")
	}
	tx, err := s.db.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var total int
	if err := tx.QueryRow(r.Context(), "SELECT count(*) "+supplierJobRows+filter, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := tx.Query(r.Context(), `SELECT j.id,j.service_id,u.id customer_id,u.display_name customer,p.name product,a.id supplier_id,a.name supplier,
 j.action,'供应商交付任务'::text description,'supplier'::text source,j.status,j.attempts,j.created_at,j.updated_at,
 (j.status='failed' AND j.action='sync' AND j.external_started_at IS NULL AND a.active AND a.running_job_id IS NULL
 AND s.upstream_id<>'' AND s.status IN ('active','suspended') AND NOT EXISTS
 (SELECT 1 FROM provision_jobs other WHERE other.service_id=s.id AND other.id<>j.id AND other.status IN ('pending','running','waiting_manual','waiting_verification'))) can_retry,
 (j.status='waiting_verification') verification_required
 `+supplierJobRows+filter+` ORDER BY j.updated_at DESC,j.id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
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
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func validSupplierJobAction(action string) bool {
	switch action {
	case "create", "suspend", "resume", "terminate", "sync", "upgrade", "reinstall", "reset_password", "reboot", "power_on", "power_off", "force_off", "force_reboot", "rescue_system", "exit_rescue_system", "snapshot_create", "snapshot_delete", "snapshot_restore", "backup_create", "backup_delete", "backup_restore", "nat_acl_create", "nat_acl_delete", "nat_web_create", "nat_web_delete", "cdrom_mount", "cdrom_unmount":
		return true
	default:
		return false
	}
}

func (s *Server) retrySupplierJob(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedAttempts  int       `json:"expected_attempts"`
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedAttempts < 0 || input.ExpectedUpdatedAt.IsZero() {
		writeError(w, 400, "invalid_retry", "任务版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	hash := settingMutationHash(struct {
		ID        int64
		Attempts  int
		UpdatedAt time.Time
	}{id, input.ExpectedAttempts, input.ExpectedUpdatedAt})
	replayed, err := s.db.RetrySupplierJob(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), id, input.ExpectedAttempts, input.ExpectedUpdatedAt, key, hash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"queued": true, "replayed": replayed})
}
