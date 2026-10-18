package httpapi

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var reportAnnualPermissions = map[string]string{
	"GET /api/v1/admin/reports/annual-cash":         "reports.view",
	"GET /api/v1/admin/reports/expenses":            "reports.view",
	"POST /api/v1/admin/reports/expenses":           "finance.manage",
	"POST /api/v1/admin/reports/expenses/{id}/void": "finance.manage",
}

func (s *Server) registerAnnualReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/reports/annual-cash", s.auth(s.annualCashReport, true))
	mux.HandleFunc("GET /api/v1/admin/reports/expenses", s.auth(s.reportExpenses, true))
	mux.HandleFunc("POST /api/v1/admin/reports/expenses", s.auth(s.createReportExpense, true))
	mux.HandleFunc("POST /api/v1/admin/reports/expenses/{id}/void", s.auth(s.voidReportExpense, true))
}

// Lock the current actor and role before resource access or idempotent replay.
// Global operating costs cannot be attributed to one salesperson's customers.
func reportExpenseAccess(r *http.Request, tx pgx.Tx, permission string) (global, manageable bool, err error) {
	var role, scope string
	var roleID *int64
	err = tx.QueryRow(r.Context(), "SELECT role,admin_role_id,customer_scope FROM users WHERE id=$1 AND status='active' FOR SHARE", identityFrom(r).ID).Scan(&role, &roleID, &scope)
	denied := func() error {
		return &txHTTPError{403, "permission_denied", "没有访问此报表或费用台账的权限"}
	}
	if err == pgx.ErrNoRows {
		return false, false, denied()
	}
	if err != nil {
		return false, false, err
	}
	if role != "super_admin" && (role != "admin" || roleID == nil) {
		return false, false, denied()
	}
	permissions := []string{}
	if role != "super_admin" {
		var active bool
		err = tx.QueryRow(r.Context(), "SELECT active,permissions FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&active, &permissions)
		if err == pgx.ErrNoRows || (err == nil && (!active || !slices.Contains(permissions, permission))) {
			return false, false, denied()
		}
		if err != nil {
			return false, false, err
		}
	}
	// Recheck the exact current credential under a row lock before replay.
	// A concurrent logout/revocation must either precede this operation or wait
	// until its result commits, just like settings mutations.
	var sessionUser int64
	if bearer := bearerCredential(r); bearer != "" {
		err = tx.QueryRow(r.Context(), "SELECT user_id FROM api_keys WHERE secret_hash=$1 AND user_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()) FOR SHARE", tokenHash(bearer), identityFrom(r).ID).Scan(&sessionUser)
	} else {
		cookie, cookieErr := r.Cookie("zjmfgo_session")
		if cookieErr != nil {
			return false, false, &txHTTPError{401, "unauthorized", "请先登录"}
		}
		err = tx.QueryRow(r.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", tokenHash(cookie.Value), identityFrom(r).ID).Scan(&sessionUser)
	}
	if err == pgx.ErrNoRows {
		return false, false, &txHTTPError{401, "unauthorized", "登录已失效"}
	}
	if err != nil {
		return false, false, err
	}
	global = role == "super_admin" || scope == "all"
	return global, global && (role == "super_admin" || slices.Contains(permissions, "finance.manage")), nil
}

type reportExpenseInput struct {
	AmountCents int64  `json:"amount_cents"`
	OccurredOn  string `json:"occurred_on"`
	Category    string `json:"category"`
	Source      string `json:"source"`
	EvidenceRef string `json:"evidence_ref"`
	Note        string `json:"note"`
}

func reportExpenseText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum || (required && value == "") {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) && char != '\n' && char != '\t' {
			return false
		}
	}
	return true
}

func validateReportExpense(in *reportExpenseInput) error {
	in.Category, in.Source, in.EvidenceRef, in.Note = strings.TrimSpace(in.Category), strings.TrimSpace(in.Source), strings.TrimSpace(in.EvidenceRef), strings.TrimSpace(in.Note)
	date, err := time.Parse("2006-01-02", in.OccurredOn)
	if err != nil || date.Year() < 2000 || date.After(time.Now().UTC().AddDate(1, 0, 0)) || in.AmountCents < 1 || in.AmountCents > 100000000000 || !reportExpenseText(in.Category, 80, true) || !reportExpenseText(in.Source, 120, true) || !reportExpenseText(in.EvidenceRef, 500, true) || !reportExpenseText(in.Note, 1000, false) {
		return &txHTTPError{400, "invalid_report_expense", "费用金额、发生日期、类别、来源或凭据无效"}
	}
	return nil
}

const reportExpenseProjection = `e.id,e.amount_cents,to_char(e.occurred_on,'YYYY-MM-DD') AS occurred_on,e.category,e.source,e.evidence_ref,e.note,e.recorded_by,u.display_name AS recorded_by_name,e.created_at,
 CASE WHEN v.expense_id IS NULL THEN 'active' ELSE 'voided' END AS status,CASE WHEN v.expense_id IS NULL THEN 1 ELSE 2 END AS version,
 v.recorded_by AS voided_by,vu.display_name AS voided_by_name,v.note AS void_note,v.created_at AS voided_at`
const reportExpenseFrom = ` FROM report_expenses e JOIN users u ON u.id=e.recorded_by LEFT JOIN report_expense_voids v ON v.expense_id=e.id LEFT JOIN users vu ON vu.id=v.recorded_by `

func (s *Server) reportExpenses(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, false)
	if s.reportInputFailure(w, r, err) {
		return
	}
	limit, offset := pagination(r)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		global, manageable, err := reportExpenseAccess(r, tx, "reports.view")
		if err != nil {
			return txResponse{}, err
		}
		if !global {
			return txResponse{}, &txHTTPError{403, "global_expenses_forbidden", "全局费用台账要求全部客户范围"}
		}
		rows, err := tx.Query(r.Context(), `WITH filtered AS (SELECT `+reportExpenseProjection+reportExpenseFrom+` WHERE e.occurred_on>=($1::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AND e.occurred_on<($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date)
 SELECT (SELECT count(*) FROM filtered) AS total,coalesce((SELECT jsonb_agg(to_jsonb(page)) FROM (SELECT * FROM filtered ORDER BY occurred_on DESC,id DESC LIMIT $3 OFFSET $4)page),'[]'::jsonb) AS items`, period.Start, period.End, limit, offset)
		if err != nil {
			return txResponse{}, err
		}
		out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
		if err != nil {
			return txResponse{}, err
		}
		out["limit"], out["offset"], out["manageable"] = limit, offset, manageable
		return txResponse{200, out}, nil
	})
}

func (s *Server) createReportExpense(w http.ResponseWriter, r *http.Request) {
	var in reportExpenseInput
	if !decode(w, r, &in) {
		return
	}
	if err := validateReportExpense(&in); err != nil {
		var failure = err.(*txHTTPError)
		writeError(w, failure.status, failure.code, failure.message)
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		global, _, err := reportExpenseAccess(r, tx, "finance.manage")
		if err != nil {
			return txResponse{}, err
		}
		if !global {
			return txResponse{}, &txHTTPError{403, "global_expenses_forbidden", "登记全局费用要求全部客户范围"}
		}
		var out map[string]any
		replayed, err := store.ReplayOperation(r.Context(), tx, actor, "report-expense", key, hash, &out)
		if err != nil {
			return txResponse{}, err
		}
		if replayed {
			out["replayed"] = true
			return txResponse{200, out}, nil
		}
		var id int64
		err = tx.QueryRow(r.Context(), `INSERT INTO report_expenses(amount_cents,occurred_on,category,source,evidence_ref,note,recorded_by) VALUES($1,$2::date,$3,$4,$5,$6,$7) RETURNING id`, in.AmountCents, in.OccurredOn, in.Category, in.Source, in.EvidenceRef, in.Note, actor).Scan(&id)
		if err != nil {
			var constraint *pgconn.PgError
			if errors.As(err, &constraint) && constraint.Code == "23505" && constraint.ConstraintName == "report_expenses_source_evidence" {
				return txResponse{}, &txHTTPError{409, "report_expense_duplicate", "同一来源和凭据已登记（含已撤销记录）；纠错请提供新的凭据标识并说明关联记录"}
			}
			return txResponse{}, err
		}
		out = map[string]any{"id": id, "version": 1, "status": "active", "replayed": false}
		if err = store.Audit(r.Context(), tx, actor, "report_expense.created", "report_expense", id, in); err != nil {
			return txResponse{}, err
		}
		if err = store.SaveOperation(r.Context(), tx, actor, "report-expense", key, hash, out); err != nil {
			return txResponse{}, err
		}
		return txResponse{201, out}, nil
	})
}

func (s *Server) voidReportExpense(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.ExpectedVersion < 1 || !reportExpenseText(in.Note, 1000, true) {
		writeError(w, 400, "invalid_report_expense_void", "请提供有效费用版本和撤销原因")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input any
	}{id, in})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		global, _, err := reportExpenseAccess(r, tx, "finance.manage")
		if err != nil {
			return txResponse{}, err
		}
		if !global {
			return txResponse{}, &txHTTPError{403, "global_expenses_forbidden", "撤销全局费用要求全部客户范围"}
		}
		var out map[string]any
		replayed, err := store.ReplayOperation(r.Context(), tx, actor, "report-expense", key, hash, &out)
		if err != nil {
			return txResponse{}, err
		}
		if replayed {
			out["replayed"] = true
			return txResponse{200, out}, nil
		}
		var current int64
		if err = tx.QueryRow(r.Context(), "SELECT id FROM report_expenses WHERE id=$1 FOR UPDATE", id).Scan(&current); err == pgx.ErrNoRows {
			return txResponse{}, &txHTTPError{404, "report_expense_not_found", "费用记录不存在"}
		} else if err != nil {
			return txResponse{}, err
		}
		var voided bool
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM report_expense_voids WHERE expense_id=$1)", id).Scan(&voided); err != nil {
			return txResponse{}, err
		}
		if voided || in.ExpectedVersion != 1 {
			return txResponse{}, &txHTTPError{409, "version_conflict", "费用已变更，请重新加载"}
		}
		if _, err = tx.Exec(r.Context(), "INSERT INTO report_expense_voids(expense_id,recorded_by,note) VALUES($1,$2,$3)", id, actor, in.Note); err != nil {
			return txResponse{}, err
		}
		out = map[string]any{"id": id, "version": 2, "status": "voided", "replayed": false}
		if err = store.Audit(r.Context(), tx, actor, "report_expense.voided", "report_expense", id, in); err != nil {
			return txResponse{}, err
		}
		if err = store.SaveOperation(r.Context(), tx, actor, "report-expense", key, hash, out); err != nil {
			return txResponse{}, err
		}
		return txResponse{201, out}, nil
	})
}
