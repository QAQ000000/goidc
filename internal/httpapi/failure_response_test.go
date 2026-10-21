package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestFailureResponseSemantics(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid input", &store.ValidationError{Code: "invalid_checkout", Message: "输入无效"}, 400, "invalid_checkout"},
		{"wrapped invalid input", fmt.Errorf("private-wrapper: %w", &store.ValidationError{Code: "invalid_contact", Message: "邮箱无效"}), 400, "invalid_contact"},
		{"unauthorized", &store.Conflict{Code: "unauthorized", Message: "请登录"}, 401, "unauthorized"},
		{"permission", &store.Conflict{Code: "permission_denied", Message: "无权限"}, 403, "permission_denied"},
		{"separate reviewer", &store.Conflict{Code: "external_refund_self_review", Message: "需要其他复核人"}, 403, "external_refund_self_review"},
		{"version conflict", &store.Conflict{Code: "version_conflict", Message: "版本已变更"}, 409, "version_conflict"},
		{"idempotency conflict", &store.Conflict{Code: "idempotency_conflict", Message: "请求内容冲突"}, 409, "idempotency_conflict"},
		{"state conflict", &store.Conflict{Code: "service_action_pending", Message: "服务有待办"}, 409, "service_action_pending"},
		{"missing", fmt.Errorf("private-wrapper: %w", pgx.ErrNoRows), 404, "not_found"},
		{"cancelled service", &pgconn.PgError{Code: "23514", Message: "service_cancellation_approved"}, 409, "service_cancellation_approved"},
		{"pending service", &pgconn.PgError{Code: "23514", Message: "service_action_pending"}, 409, "service_action_pending"},
		{"pending payment", &pgconn.PgError{Code: "23514", Message: "invoice_payment_pending"}, 409, "invoice_payment_pending"},
		{"refund hold", &pgconn.PgError{Code: "23514", Message: "renewal_refund_hold"}, 409, "renewal_refund_hold"},
		{"unique constraint", &pgconn.PgError{Code: "23505", Message: "private-database-detail"}, 409, "constraint_conflict"},
		{"foreign constraint", &pgconn.PgError{Code: "23503", Message: "private-database-detail"}, 409, "constraint_conflict"},
		{"check constraint", &pgconn.PgError{Code: "23514", Message: "private-database-detail"}, 409, "constraint_conflict"},
		{"unexpected database error", &pgconn.PgError{Code: "XX000", Message: "private-database-detail"}, 500, "internal_error"},
		{"deadline", context.DeadlineExceeded, 500, "internal_error"},
		{"cancelled", context.Canceled, 500, "internal_error"},
		{"unexpected error", errors.New("private-internal-detail"), 500, "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Server{log: slog.New(slog.DiscardHandler)}
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-ID", "0123456789abcdef0123456789abcdef")
			s.fail(w, httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil), test.err)
			body := decodeJSONTestError(t, w)
			if w.Code != test.status || body["code"] != test.code || body["correlation_id"] != w.Header().Get("X-Request-ID") {
				t.Fatalf("response = %d %+v", w.Code, body)
			}
			if strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("response disclosed internal detail: %s", w.Body.String())
			}
		})
	}
}

func TestInputValidationHTTPHandlers(t *testing.T) {
	// Exercise the real handlers and Store validation with no database pool.
	// Authentication is supplied explicitly; middleware authorization has its
	// own database regressions and is not part of this unit test.
	s := &Server{db: &store.Store{}, log: slog.New(slog.DiscardHandler)}
	for _, test := range []struct {
		name, path, body, code string
		handler                http.HandlerFunc
	}{
		{"checkout version", "/api/v1/orders", `{"cart_version":0,"item_ids":[1]}`, "invalid_checkout", s.createOrder},
		{"checkout duplicate selection", "/api/v1/orders", `{"cart_version":1,"item_ids":[1,1]}`, "invalid_selection", s.createOrder},
		{"contact email", "/api/v1/contacts", `{"email":"invalid"}`, "invalid_contact", s.createContact},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "validation-test-key")
			r = r.WithContext(context.WithValue(r.Context(), userKey, identity{ID: 1, Role: "customer"}))
			w := httptest.NewRecorder()
			test.handler(w, r)
			body := decodeJSONTestError(t, w)
			if w.Code != http.StatusBadRequest || body["code"] != test.code {
				t.Fatalf("response = %d %+v", w.Code, body)
			}
		})
	}
}
