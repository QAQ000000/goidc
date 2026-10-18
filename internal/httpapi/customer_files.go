package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const customerFileColumns = `f.id,f.customer_id,f.title,f.remarks,f.filename,f.mime_type,f.size_bytes,f.sha256,f.downloads,f.created_at,f.updated_at`
const adminCustomerFileColumns = customerFileColumns + `,f.version,f.uploader_id,u.display_name AS uploader_name,c.display_name AS customer_name,c.username AS customer_username`

func (s *Server) customerFileCredentialCurrent(w http.ResponseWriter, r *http.Request, tx pgx.Tx, uid int64) bool {
	fresh, err := s.publicDownloadIdentity(r, tx)
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	if fresh != uid {
		writeError(w, 401, "unauthorized", "登录已失效")
		return false
	}
	return true
}

func (s *Server) customerFileTx(w http.ResponseWriter, r *http.Request, manage bool) (pgx.Tx, int64, bool) {
	tx, err := s.db.CustomerFileTx(r.Context(), identityFrom(r).ID, adminRoute(r), manage)
	if err != nil {
		s.fail(w, r, err)
		return nil, 0, false
	}
	uid, err := s.publicDownloadIdentity(r, tx)
	if err != nil {
		tx.Rollback(r.Context())
		s.fail(w, r, err)
		return nil, 0, false
	}
	if uid == 0 || uid != identityFrom(r).ID {
		tx.Rollback(r.Context())
		writeError(w, 401, "unauthorized", "登录已失效")
		return nil, 0, false
	}
	if !adminRoute(r) {
		var enabled bool
		if err = tx.QueryRow(r.Context(), "SELECT enabled FROM download_settings WHERE id=1").Scan(&enabled); err != nil {
			tx.Rollback(r.Context())
			s.fail(w, r, err)
			return nil, 0, false
		}
		if !enabled {
			tx.Rollback(r.Context())
			writeError(w, 403, "downloads_disabled", "文件下载已关闭")
			return nil, 0, false
		}
	}
	return tx, uid, true
}
func (s *Server) customerFileOwners(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r).ID
	s.list(w, r, `SELECT id,username,display_name,status FROM users WHERE role='customer' AND admin_customer_allowed($1,id)
 AND (username ILIKE '%'||$2||'%' OR display_name ILIKE '%'||$2||'%' OR id::text=$2) ORDER BY id LIMIT 100`, id, strings.TrimSpace(r.URL.Query().Get("q")))
}
func (s *Server) customerFiles(w http.ResponseWriter, r *http.Request) {
	admin := adminRoute(r)
	var filter *int64
	if raw := r.URL.Query().Get("customer_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			writeError(w, 400, "invalid_customer", "客户编号无效")
			return
		}
		filter = &id
	}
	tx, uid, ok := s.customerFileTx(w, r, false)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var columns, scope string
	if admin {
		if filter != nil {
			if err := store.LockAdminCustomer(r.Context(), tx, uid, *filter); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		columns = adminCustomerFileColumns
		scope = "admin_customer_allowed($1,f.customer_id)"
	} else {
		columns = customerFileColumns
		scope = "f.customer_id=$1"
	}
	limit, offset := pagination(r)
	if !s.customerFileCredentialCurrent(w, r, tx, uid) {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	where := ` FROM customer_files f JOIN users c ON c.id=f.customer_id JOIN users u ON u.id=f.uploader_id WHERE ` + scope + `
 AND ($2::bigint IS NULL OR f.customer_id=$2) AND ($3='' OR f.title ILIKE '%'||$3||'%' OR f.remarks ILIKE '%'||$3||'%' OR f.filename ILIKE '%'||$3||'%')`
	page, err := listPage(r.Context(), tx, "SELECT count(*)"+where,
		"SELECT "+columns+where+" ORDER BY f.id DESC", []any{uid, filter, query}, limit, offset, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}
func (s *Server) adminCustomerFileDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, uid, ok := s.customerFileTx(w, r, false)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var owner int64
	if err := tx.QueryRow(r.Context(), "SELECT customer_id FROM customer_files WHERE id=$1", id).Scan(&owner); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := store.LockAdminCustomer(r.Context(), tx, uid, owner); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := tx.Query(r.Context(), "SELECT "+adminCustomerFileColumns+" FROM customer_files f JOIN users c ON c.id=f.customer_id JOIN users u ON u.id=f.uploader_id WHERE f.id=$1", id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.customerFileCredentialCurrent(w, r, tx, uid) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}
func (s *Server) saveCustomerFile(w http.ResponseWriter, r *http.Request) {
	var id int64
	var ok bool
	if r.Method == "PUT" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in store.CustomerFileInput
	file, ok := s.readDownloadUpload(w, r, &in)
	if !ok {
		return
	}
	tx, uid, ok := s.customerFileTx(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	saved, err := s.db.SaveCustomerFile(requestFileContext(r), tx, uid, id, in, file)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.customerFileCredentialCurrent(w, r, tx, uid) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]int64{"id": saved})
}
func (s *Server) deleteCustomerFiles(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Items []store.CustomerFileVersion `json:"items"`
	}
	if r.Method == "DELETE" {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var version struct {
			ExpectedVersion int64 `json:"expected_version"`
		}
		if !decode(w, r, &version) {
			return
		}
		input.Items = []store.CustomerFileVersion{{ID: id, ExpectedVersion: version.ExpectedVersion}}
	} else {
		if !decode(w, r, &input) {
			return
		}
	}
	tx, uid, ok := s.customerFileTx(w, r, true)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	if err := store.DeleteCustomerFiles(r.Context(), tx, uid, input.Items); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.customerFileCredentialCurrent(w, r, tx, uid) {
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]int{"deleted": len(input.Items)})
}
func (s *Server) downloadCustomerFile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, uid, ok := s.customerFileTx(w, r, false)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	var owner int64
	if err := tx.QueryRow(r.Context(), "SELECT customer_id FROM customer_files WHERE id=$1", id).Scan(&owner); err != nil {
		s.fail(w, r, err)
		return
	}
	if adminRoute(r) {
		if err := store.LockAdminCustomer(r.Context(), tx, uid, owner); err != nil {
			s.fail(w, r, err)
			return
		}
	} else if owner != uid {
		writeError(w, 404, "not_found", "文件不存在")
		return
	}
	var name, mt, hash string
	if !s.customerFileCredentialCurrent(w, r, tx, uid) {
		return
	}
	var content []byte
	var size int64
	var objectID *string
	if err := tx.QueryRow(r.Context(), "SELECT filename,mime_type,size_bytes,sha256,content,storage_object_id FROM customer_files WHERE id=$1", id).Scan(&name, &mt, &size, &hash, &content, &objectID); err != nil {
		s.fail(w, r, err)
		return
	}
	content, err := s.db.ReadFileContent(r.Context(), tx, "customer_files", content, objectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.customerFileCredentialCurrent(w, r, tx, uid) {
		return
	}
	if r.Method != "HEAD" && storedFileTransfersBytes(r, name, mt, hash, content) {
		if _, err := tx.Exec(r.Context(), "UPDATE customer_files SET downloads=downloads+1 WHERE id=$1", id); err != nil {
			s.fail(w, r, err)
			return
		}
		if err := store.Audit(r.Context(), tx, uid, "customer_file.downloaded", "customer_file", id, map[string]any{"customer_id": owner, "filename": name, "size_bytes": size, "admin": adminRoute(r)}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	serveStoredFile(w, r, name, mt, hash, content)
}
