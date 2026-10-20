package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
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
func reportExpenseAccess(r *http.Request, tx pgx.Tx, permission string) (bool, bool, error) {
	global, manageable, err := store.ReportExpenseAccess(requestFileContext(r), tx, identityFrom(r).ID, permission)
	var failure *store.AccountBusinessError
	if errors.As(err, &failure) {
		return false, false, &txHTTPError{failure.Status, failure.Code, failure.Message}
	}
	return global, manageable, err
}

type reportExpenseInput = store.ReportExpenseInput

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

func (s *Server) reportExpenses(w http.ResponseWriter, r *http.Request) {
	period, err := parseReportPeriod(r, false)
	if s.reportInputFailure(w, r, err) {
		return
	}
	limit, offset := pagination(r)
	out, _, err := s.db.ReportExpenses(requestFileContext(r), identityFrom(r).ID, period.Start, period.End, limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, 200, out)
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
	out, replayed, err := s.db.CreateReportExpense(requestFileContext(r), actor, key, hash, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := 201
	if replayed {
		status = 200
	}
	write(w, status, out)
}

func (s *Server) voidReportExpense(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.ReportExpenseVoidInput
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
	out, replayed, err := s.db.VoidReportExpense(requestFileContext(r), actor, id, key, hash, in)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := 201
	if replayed {
		status = 200
	}
	write(w, status, out)
}
