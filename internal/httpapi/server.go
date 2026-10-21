package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codex/zjmfgo/internal/accesslog"
	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Server struct {
	db          *store.Store
	cfg         config.Config
	log         *slog.Logger
	accessLogMu sync.RWMutex
	accessLog   *accesslog.Sink
}

func New(db *store.Store, cfg config.Config, log *slog.Logger) *Server {
	db.SupplierDownstreamURL = cfg.PublicOrigin
	db.Provisioners = cfg.NOKVMProfiles
	db.ProvisionEncryptionKey = cfg.ProvisionEncryptionKey
	db.PurchaseFieldEncryptionKey = cfg.PurchaseFieldEncryptionKey
	if cfg.FileStorage != nil {
		db.FileStorage = cfg.FileStorage
	}
	return &Server{db: db, cfg: cfg, log: log}
}

type identity struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	Name        string   `json:"display_name"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}
type ctxKey int

const userKey ctxKey = 1
const apiKeyAuthKey ctxKey = 2

func identityFrom(r *http.Request) identity { return r.Context().Value(userKey).(identity) }
func bearerCredential(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "zjmfgo_") || len(parts[1]) != 71 {
		return ""
	}
	return parts[1]
}
func apiKeyRequest(r *http.Request) bool {
	value, _ := r.Context().Value(apiKeyAuthKey).(bool)
	return value
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerNotificationCenterRoutes(mux)
	s.registerRuntimeAndAuthRoutes(mux)
	s.registerAdminSiteAndOperationsRoutes(mux)
	s.registerAdminSettingsRoutes(mux)
	s.registerPublicContentRoutes(mux)
	s.registerFileRoutes(mux)
	s.registerCustomerCommerceRoutes(mux)
	s.registerAccountBillingRoutes(mux)
	s.registerAccountSecurityRoutes(mux)
	s.registerPaymentRoutes(mux)
	s.registerServiceLifecycleRoutes(mux)
	s.registerCustomerTicketRoutes(mux)
	s.registerContactRoutes(mux)
	s.registerAdminAccessAndMigrationRoutes(mux)
	s.registerAdminNotificationRoutes(mux)
	s.registerAdminReportRoutes(mux)
	s.registerAdminCatalogRoutes(mux)
	s.registerAdminContentRoutes(mux)
	s.registerAdminCustomerReadRoutes(mux)
	s.registerAdminOrderRoutes(mux)
	s.registerAdminInvoiceRoutes(mux)
	s.registerAdminCustomerMutationRoutes(mux)
	s.registerAdminServiceRoutes(mux)
	s.registerAdminTicketRoutes(mux)
	s.registerAdminAuditRoutes(mux)
	return s.correlateRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		isWebhook := r.Method == "POST" && ((strings.HasPrefix(r.URL.Path, "/api/v1/payments/") && strings.HasSuffix(r.URL.Path, "/webhook")) || r.URL.Path == "/api/host/sync" || r.URL.Path == "/api/v1/host/sync" || notificationWebhookPath(r.URL.Path))
		if !isWebhook && r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && (bearerCredential(r) == "" || strings.HasPrefix(r.URL.Path, "/api/v1/auth/")) {
			if r.Header.Get("X-Requested-With") != "zjmfgo" {
				writeError(w, 403, "csrf_required", "请求校验失败")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
				writeError(w, 403, "origin_denied", "请求来源不允许")
				return
			}
		}
		if portal := r.Header.Get(authPortalHeader); portal != "" && portal != "customer" && portal != "admin" {
			writeError(w, http.StatusBadRequest, "invalid_portal", "登录入口无效")
			return
		}
		r = s.projectBrowserSession(r, selectedBrowserPortal(r))
		if s.maintenanceAllowed(w, r) {
			defer rememberRequestRoute(r)
			mux.ServeHTTP(w, r)
		}
	}))
}

func (s *Server) originAllowed(origin string) bool {
	if len(s.cfg.PublicOrigins) == 0 {
		return origin == s.cfg.PublicOrigin
	}
	for _, allowed := range s.cfg.PublicOrigins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func (s *Server) auth(next http.HandlerFunc, admin bool) http.HandlerFunc {
	return s.authWith(next, admin, false)
}
func (s *Server) sessionAuth(next http.HandlerFunc, admin bool) http.HandlerFunc {
	return s.authWith(next, admin, true)
}

type authAudience uint8

const (
	customerAudience authAudience = iota
	adminAudience
	accountAudience
)

// Account security, personal notifications and the explicitly shared ticket
// operations accept either account type. Business routes default to customers;
// granting an administrator more permissions must never grant customer access.
func (s *Server) accountAuth(next http.HandlerFunc, sessionOnly bool) http.HandlerFunc {
	return s.authenticate(next, accountAudience, sessionOnly)
}

func (s *Server) authWith(next http.HandlerFunc, admin, sessionOnly bool) http.HandlerFunc {
	audience := customerAudience
	if admin {
		audience = adminAudience
	}
	return s.authenticate(next, audience, sessionOnly)
}

func (s *Server) authenticate(next http.HandlerFunc, audience authAudience, sessionOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bearer := bearerCredential(r)
		if sessionOnly && bearer != "" {
			writeError(w, http.StatusForbidden, "session_required", "该操作需要浏览器会话")
			return
		}
		var resolved store.HTTPIdentity
		var err error
		if bearer != "" {
			resolved, err = s.db.APIKeyIdentity(r.Context(), tokenHash(bearer))
		} else {
			portal := requestPortal(r)
			if audience == customerAudience {
				portal = "customer"
			} else if audience == adminAudience {
				portal = "admin"
			}
			r = s.projectBrowserSession(r, portal)
			resolved, err = browserIdentity(r)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 401, "unauthorized", "登录已失效")
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		id := identity{ID: resolved.ID, Username: resolved.Username, Email: resolved.Email, Name: resolved.Name, Role: resolved.Role}
		if bearer != "" && id.Role == "customer" {
			enabled, enabledErr := s.db.CustomerResourceAPIEnabled(r.Context())
			if enabledErr != nil {
				s.fail(w, r, enabledErr)
				return
			}
			if !enabled {
				writeError(w, http.StatusForbidden, "api_access_disabled", "客户 API 接口暂未开放")
				return
			}
		}
		if audience == customerAudience && id.Role != "customer" {
			writeError(w, http.StatusForbidden, "customer_required", "该功能仅供客户账户使用，请返回管理后台")
			return
		}
		if audience == adminAudience && id.Role != "admin" && id.Role != "super_admin" {
			writeError(w, 403, "forbidden", "需要管理员权限")
			return
		}
		id.Permissions = []string{}
		if id.Role == "admin" {
			id.Permissions, err = s.db.AdminGrants(r.Context(), id.ID)
			if err != nil {
				s.fail(w, r, err)
				return
			}
		} else if id.Role == "super_admin" {
			for _, p := range store.ActiveAdminPermissions() {
				id.Permissions = append(id.Permissions, p.Code)
			}
		}
		if audience == adminAudience {
			permission, known := adminRoutePermissions[r.Pattern]
			allowed := id.can(permission)
			if r.Pattern == "GET /api/v1/admin/customer-field-definitions" {
				allowed = id.can("customers.view") || id.can("customers.manage")
			}
			if !known || (permission == "@super" && id.Role != "super_admin") || (permission != "@super" && !allowed) {
				writeError(w, 403, "permission_denied", "没有执行此操作的权限")
				return
			}
		}
		ctx := context.WithValue(r.Context(), userKey, id)
		if bearer != "" {
			ctx = context.WithValue(ctx, apiKeyAuthKey, true)
		}
		started := time.Now()
		recorder := &apiLogResponseWriter{ResponseWriter: w, status: http.StatusOK}
		next(recorder, r.WithContext(ctx))
		// Diagnostics are best effort and never wait for queue capacity or IO.
		// Only non-secret value fields survive this request; business audits stay
		// in their original transactions.
		s.enqueueAPIAccess(r.WithContext(ctx), id.ID, recorder.status, started)
	}
}

type apiLogResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *apiLogResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *apiLogResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *apiLogResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Pool.Ping(ctx); err != nil {
		writeError(w, 503, "database_unavailable", "数据库不可用")
		return
	}
	write(w, 200, map[string]string{"status": "ready", "database": "postgresql18"})
}
func write(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"data": data})
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeErrorDetails(w, status, code, message, nil)
}
func writeErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	errorBody := map[string]any{"code": code, "message": message}
	for key, value := range details {
		errorBody[key] = value
	}
	if id := w.Header().Get("X-Request-ID"); id != "" {
		errorBody["correlation_id"] = id
	}
	json.NewEncoder(w).Encode(map[string]any{"error": errorBody})
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *store.ValidationError
	if errors.As(err, &invalid) {
		writeError(w, http.StatusBadRequest, invalid.Code, invalid.Message)
		return
	}
	var c *store.Conflict
	if errors.As(err, &c) {
		if c.Code == "unauthorized" {
			writeError(w, 401, c.Code, c.Message)
			return
		}
		if c.Code == "permission_denied" || c.Code == "external_refund_self_review" {
			writeError(w, 403, c.Code, c.Message)
			return
		}
		writeError(w, 409, c.Code, c.Message)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found", "记录不存在")
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23514" {
		switch pg.Message {
		case "service_cancellation_approved":
			writeError(w, 409, pg.Message, "服务取消已获批准，不能继续续费或付款，请联系管理员")
			return
		case "service_action_pending":
			writeError(w, 409, pg.Message, "服务正在处理其他操作，请刷新后重试")
			return
		case "invoice_payment_pending":
			writeError(w, 409, pg.Message, "续费账单存在未核实的在线支付，请先确认支付结果")
			return
		}
	}
	if errors.As(err, &pg) && pg.Code == "23514" && pg.Message == "renewal_refund_hold" {
		writeError(w, 409, "renewal_refund_hold", "服务存在待复核续费退款，核实后再操作")
		return
	}
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514") {
		writeError(w, 409, "constraint_conflict", "记录重复或关联数据无效")
		return
	}
	code := "internal_error"
	if errors.As(err, &pg) {
		code = pg.Code
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline_exceeded"
	} else if errors.Is(err, context.Canceled) {
		code = "context_cancelled"
	}
	correlation.Logger(r.Context(), s.log).Error("request failed", "method", r.Method, "route", requestRoute(r), "error_code", code)
	writeError(w, 500, "internal_error", "请求处理失败")
}
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid_id", "记录编号无效")
		return 0, false
	}
	return id, true
}
func pagination(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 25
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, query string, args ...any) {
	rows, err := s.db.Pool.Query(r.Context(), query, args...)
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
	write(w, 200, items)
}
