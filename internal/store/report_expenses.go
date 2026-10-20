package store

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"slices"
	"time"
)

func ReportExpenseAccess(ctx context.Context, tx pgx.Tx, actor int64, permission string) (global, manageable bool, err error) {
	var role, scope string
	var roleID *int64
	err = tx.QueryRow(ctx, "SELECT role,admin_role_id,customer_scope FROM administrators WHERE id=$1 AND status='active' FOR SHARE", actor).Scan(&role, &roleID, &scope)
	denied := func() error {
		return &AccountBusinessError{403, "permission_denied", "没有访问此报表或费用台账的权限"}
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
		err = tx.QueryRow(ctx, "SELECT active,permissions FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&active, &permissions)
		if err == pgx.ErrNoRows || (err == nil && (!active || !slices.Contains(permissions, permission))) {
			return false, false, denied()
		}
		if err != nil {
			return false, false, err
		}
	}
	if err = lockStorageCredential(ctx, tx, actor); err != nil {
		return false, false, err
	}
	global = role == "super_admin" || scope == "all"
	return global, global && (role == "super_admin" || slices.Contains(permissions, "finance.manage")), nil
}

type ReportExpenseInput struct {
	AmountCents int64  `json:"amount_cents"`
	OccurredOn  string `json:"occurred_on"`
	Category    string `json:"category"`
	Source      string `json:"source"`
	EvidenceRef string `json:"evidence_ref"`
	Note        string `json:"note"`
}

const reportExpenseProjection = `e.id,e.amount_cents,to_char(e.occurred_on,'YYYY-MM-DD') AS occurred_on,e.category,e.source,e.evidence_ref,e.note,e.recorded_by,u.display_name AS recorded_by_name,e.created_at,
 CASE WHEN v.expense_id IS NULL THEN 'active' ELSE 'voided' END AS status,CASE WHEN v.expense_id IS NULL THEN 1 ELSE 2 END AS version,
 v.recorded_by AS voided_by,vu.display_name AS voided_by_name,v.note AS void_note,v.created_at AS voided_at`
const reportExpenseFrom = ` FROM report_expenses e JOIN administrators u ON u.id=e.recorded_by LEFT JOIN report_expense_voids v ON v.expense_id=e.id LEFT JOIN administrators vu ON vu.id=v.recorded_by `

func (s *Store) ReportExpenses(ctx context.Context, actor int64, start, end time.Time, limit, offset int) (map[string]any, bool, error) {
	tx, beginErr := s.Pool.Begin(ctx)
	if beginErr != nil {
		return nil, false, beginErr
	}
	defer tx.Rollback(ctx)

	global, manageable, err := ReportExpenseAccess(ctx, tx, actor, "reports.view")
	if err != nil {
		return nil, false, err
	}
	if !global {
		return nil, false, &AccountBusinessError{403, "global_expenses_forbidden", "全局费用台账要求全部客户范围"}
	}
	rows, err := tx.Query(ctx, `WITH filtered AS (SELECT `+reportExpenseProjection+reportExpenseFrom+` WHERE e.occurred_on>=($1::timestamptz AT TIME ZONE 'Asia/Shanghai')::date AND e.occurred_on<($2::timestamptz AT TIME ZONE 'Asia/Shanghai')::date)
 SELECT (SELECT count(*) FROM filtered) AS total,coalesce((SELECT jsonb_agg(to_jsonb(page)) FROM (SELECT * FROM filtered ORDER BY occurred_on DESC,id DESC LIMIT $3 OFFSET $4)page),'[]'::jsonb) AS items`, start, end, limit, offset)
	if err != nil {
		return nil, false, err
	}
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, false, err
	}
	out["limit"], out["offset"], out["manageable"] = limit, offset, manageable
	return out, true, tx.Commit(ctx)
}
func (s *Store) CreateReportExpense(ctx context.Context, actor int64, key, hash string, in ReportExpenseInput) (map[string]any, bool, error) {
	tx, beginErr := s.Pool.Begin(ctx)
	if beginErr != nil {
		return nil, false, beginErr
	}
	defer tx.Rollback(ctx)

	global, _, err := ReportExpenseAccess(ctx, tx, actor, "finance.manage")
	if err != nil {
		return nil, false, err
	}
	if !global {
		return nil, false, &AccountBusinessError{403, "global_expenses_forbidden", "登记全局费用要求全部客户范围"}
	}
	var out map[string]any
	replayed, err := ReplayOperation(ctx, tx, actor, "report-expense", key, hash, &out)
	if err != nil {
		return nil, false, err
	}
	if replayed {
		out["replayed"] = true
		return out, true, tx.Commit(ctx)
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO report_expenses(amount_cents,occurred_on,category,source,evidence_ref,note,recorded_by) VALUES($1,$2::date,$3,$4,$5,$6,$7) RETURNING id`, in.AmountCents, in.OccurredOn, in.Category, in.Source, in.EvidenceRef, in.Note, actor).Scan(&id)
	if err != nil {
		var constraint *pgconn.PgError
		if errors.As(err, &constraint) && constraint.Code == "23505" && constraint.ConstraintName == "report_expenses_source_evidence" {
			return nil, false, &AccountBusinessError{409, "report_expense_duplicate", "同一来源和凭据已登记（含已撤销记录）；纠错请提供新的凭据标识并说明关联记录"}
		}
		return nil, false, err
	}
	out = map[string]any{"id": id, "version": 1, "status": "active", "replayed": false}
	if err = Audit(ctx, tx, actor, "report_expense.created", "report_expense", id, in); err != nil {
		return nil, false, err
	}
	if err = SaveOperation(ctx, tx, actor, "report-expense", key, hash, out); err != nil {
		return nil, false, err
	}
	return out, false, tx.Commit(ctx)
}

type ReportExpenseVoidInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note"`
}

func (s *Store) VoidReportExpense(ctx context.Context, actor, id int64, key, hash string, in ReportExpenseVoidInput) (map[string]any, bool, error) {
	tx, beginErr := s.Pool.Begin(ctx)
	if beginErr != nil {
		return nil, false, beginErr
	}
	defer tx.Rollback(ctx)

	global, _, err := ReportExpenseAccess(ctx, tx, actor, "finance.manage")
	if err != nil {
		return nil, false, err
	}
	if !global {
		return nil, false, &AccountBusinessError{403, "global_expenses_forbidden", "撤销全局费用要求全部客户范围"}
	}
	var out map[string]any
	replayed, err := ReplayOperation(ctx, tx, actor, "report-expense", key, hash, &out)
	if err != nil {
		return nil, false, err
	}
	if replayed {
		out["replayed"] = true
		return out, true, tx.Commit(ctx)
	}
	var current int64
	if err = tx.QueryRow(ctx, "SELECT id FROM report_expenses WHERE id=$1 FOR UPDATE", id).Scan(&current); err == pgx.ErrNoRows {
		return nil, false, &AccountBusinessError{404, "report_expense_not_found", "费用记录不存在"}
	} else if err != nil {
		return nil, false, err
	}
	var voided bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM report_expense_voids WHERE expense_id=$1)", id).Scan(&voided); err != nil {
		return nil, false, err
	}
	if voided || in.ExpectedVersion != 1 {
		return nil, false, &AccountBusinessError{409, "version_conflict", "费用已变更，请重新加载"}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO report_expense_voids(expense_id,recorded_by,note) VALUES($1,$2,$3)", id, actor, in.Note); err != nil {
		return nil, false, err
	}
	out = map[string]any{"id": id, "version": 2, "status": "voided", "replayed": false}
	if err = Audit(ctx, tx, actor, "report_expense.voided", "report_expense", id, in); err != nil {
		return nil, false, err
	}
	if err = SaveOperation(ctx, tx, actor, "report-expense", key, hash, out); err != nil {
		return nil, false, err
	}
	return out, false, tx.Commit(ctx)
}
