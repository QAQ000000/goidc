package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// supplierHostSync is intentionally outside the authenticated v1 API. The
// upstream provider calls this endpoint server-to-server with the legacy
// signed payload, so session cookies and browser origin checks do not apply.
func (s *Server) supplierHostSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		supplierCallbackResponse(w, http.StatusMethodNotAllowed, "请求方法不允许")
		return
	}
	in, err := decodeSupplierHostSync(r)
	if err != nil {
		supplierCallbackResponse(w, http.StatusBadRequest, "回调内容无效")
		return
	}
	if err = s.db.SyncSupplierHost(r.Context(), in); err != nil {
		status, message := http.StatusInternalServerError, "回调处理失败"
		var conflict *store.Conflict
		var pgError *pgconn.PgError
		if errors.As(err, &conflict) || errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgError) && (pgError.Code == "23505" || pgError.Code == "23514")) {
			status, message = http.StatusBadRequest, "回调校验失败"
		}
		supplierCallbackResponse(w, status, message)
		return
	}
	supplierCallbackResponse(w, http.StatusOK, "更新成功")
}

func decodeSupplierHostSync(r *http.Request) (store.SupplierHostSyncInput, error) {
	var in store.SupplierHostSyncInput
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && r.Header.Get("Content-Type") != "" {
		return in, err
	}
	if contentType == "application/json" {
		return in, json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&in)
	}
	if contentType != "" && contentType != "application/x-www-form-urlencoded" {
		return in, &callbackDecodeError{}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
	if err != nil {
		return in, err
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return in, err
	}
	first := func(key string) string { return values.Get(key) }
	in.ID, in.HostID = first("id"), first("host_id")
	in.Domain, in.DomainStatus = first("domain"), first("domainstatus")
	in.Username, in.Password = first("username"), first("password")
	in.DedicatedIP, in.AssignedIPs = first("dedicatedip"), first("assignedips")
	in.Port, in.OS, in.OSURL = first("port"), first("os"), first("os_url")
	in.NextDueDate, in.SuspendReason = first("nextduedate"), first("suspendreason")
	in.Type, in.Signature, in.RandStr = first("type"), first("signature"), first("rand_str")
	return in, nil
}

type callbackDecodeError struct{}

func (*callbackDecodeError) Error() string { return "unsupported callback content type" }

func supplierCallbackResponse(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "msg": message})
}
