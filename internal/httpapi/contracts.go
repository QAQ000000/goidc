package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (s *Server) contractTx(w http.ResponseWriter, r *http.Request, manage, templates bool) (pgx.Tx, int64, bool) {
	uid := identityFrom(r).ID
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return nil, 0, false
	}
	*r = *r.WithContext(store.WithStorageCredential(r.Context(), tokenHash(cookie.Value), false))
	tx, err := s.db.ContractTx(r.Context(), uid, adminRoute(r), manage, templates)
	if err != nil {
		s.fail(w, r, err)
		return nil, 0, false
	}
	fresh, err := s.publicDownloadIdentity(r, tx)
	if err != nil || fresh != uid {
		tx.Rollback(r.Context())
		writeError(w, 401, "unauthorized", "登录已失效")
		return nil, 0, false
	}
	return tx, uid, true
}
func contractScope(admin bool) string {
	if admin {
		return "admin_customer_allowed($1,c.customer_id)"
	}
	return "c.customer_id=$1"
}
func (s *Server) contractOwner(r *http.Request, tx pgx.Tx, uid, id int64) error {
	var owner int64
	if err := tx.QueryRow(r.Context(), "SELECT customer_id FROM contracts WHERE id=$1", id).Scan(&owner); err != nil {
		return err
	}
	if adminRoute(r) {
		return store.LockAdminCustomer(r.Context(), tx, uid, owner)
	}
	if owner != uid {
		return pgx.ErrNoRows
	}
	return nil
}
func (s *Server) contractRead(w http.ResponseWriter, r *http.Request) {
	templates := strings.Contains(r.URL.Path, "contract-templates")
	available := strings.HasSuffix(r.URL.Path, "available-templates")
	tx, uid, ok := s.contractTx(w, r, false, templates)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	limit, offset := pagination(r)
	args := []any{uid}
	where, cols := " FROM contracts c WHERE "+contractScope(adminRoute(r)), "c.*,(SELECT status FROM services WHERE id=c.service_id) AS service_status"
	if templates || available {
		cols = "c.*"
		if available {
			cols = "c.id,c.name,c.version"
		}
		where = " FROM contract_templates c WHERE ($1::bigint>0)"
		if !adminRoute(r) || available {
			where += " AND enabled"
		}
	}

	// Template settings use a dedicated branch: one statement snapshot and no
	// contract history/document lookups for template IDs.
	if templates {
		id := int64(0)
		if r.PathValue("id") != "" {
			var valid bool
			id, valid = pathID(w, r)
			if !valid {
				return
			}
		}
		if id > 0 {
			rows, err := tx.Query(r.Context(), "SELECT c.*"+where+" AND c.id=$2", uid, id)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if err = tx.Commit(r.Context()); err != nil {
				s.fail(w, r, err)
				return
			}
			write(w, 200, item)
			return
		}
		q, status := "", "all"
		// Customer template visibility and available-templates keep their existing
		// enabled-only semantics. New filters are the admin settings list contract.
		if adminRoute(r) {
			q = strings.TrimSpace(r.URL.Query().Get("q"))
			if len(q) > 120 {
				writeError(w, 400, "invalid_query", "搜索条件过长")
				return
			}
			if value := r.URL.Query().Get("status"); value != "" {
				status = value
			}
			if status != "all" && status != "enabled" && status != "disabled" {
				writeError(w, 400, "invalid_status", "模板状态筛选无效")
				return
			}
		}
		var items json.RawMessage
		var total int64
		err := tx.QueryRow(r.Context(), `WITH filtered AS (SELECT c.*`+where+` AND ($2='' OR c.name ILIKE '%'||$2||'%' OR c.content ILIKE '%'||$2||'%') AND ($3='all' OR c.enabled=($3='enabled'))), page AS (SELECT * FROM filtered ORDER BY id DESC LIMIT $4 OFFSET $5) SELECT coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY id DESC) FROM page),'[]'::jsonb),(SELECT count(*) FROM filtered)`, uid, q, status, limit, offset).Scan(&items, &total)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
		return
	}
	eligible := strings.HasSuffix(r.URL.Path, "eligible-services")
	if eligible {
		cols = "c.id,c.user_id,u.display_name AS customer_name,u.email AS customer_email,oi.product_name,c.name,c.domain,c.status,i.paid_at"
		scope := "c.user_id=$1"
		if adminRoute(r) {
			scope = "admin_customer_allowed($1,c.user_id)"
		}
		where = " FROM services c JOIN users u ON u.id=c.user_id JOIN order_items oi ON oi.id=c.order_item_id JOIN invoices i ON i.order_id=c.order_id WHERE " + scope + " AND c.status IN('active','suspended') AND i.status='paid' AND i.paid_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM contracts x WHERE x.service_id=c.id AND x.status<>'void')"
	}
	if !templates && !eligible && !available {
		q := r.URL.Query()
		if q.Get("from") != "" && q.Get("to") != "" && q.Get("from") > q.Get("to") {
			writeError(w, 400, "invalid_date_range", "起始日期不可晚于截止日期")
			return
		}
		for _, field := range []string{"q", "status", "service_status", "from", "to"} {
			v := strings.TrimSpace(q.Get(field))
			if v == "" {
				continue
			}
			args = append(args, v)
			n := len(args)
			switch field {
			case "q":
				where += fmt.Sprintf(" AND (c.template_name ILIKE '%%'||$%d||'%%' OR c.customer_name ILIKE '%%'||$%d||'%%' OR c.domain ILIKE '%%'||$%d||'%%' OR c.id::text=$%d OR concat_ws(' ',c.customer_email,c.product_name,c.service_name) ILIKE '%%'||$%d||'%%')", n, n, n, n, n)
			case "status":
				where += fmt.Sprintf(" AND c.status=$%d", n)
			case "service_status":
				where += fmt.Sprintf(" AND EXISTS(SELECT 1 FROM services ss WHERE ss.id=c.service_id AND ss.status=$%d)", n)
			default:
				if _, err := time.Parse("2006-01-02", v); err != nil {
					writeError(w, 400, "invalid_date", "日期无效")
					return
				}
				op := ">="
				suffix := ""
				if field == "to" {
					op = "<"
					suffix = " + interval '1 day'"
				}
				where += fmt.Sprintf(" AND (c.paid_at AT TIME ZONE 'Asia/Shanghai') %s $%d::date%s", op, n, suffix)
			}
		}
	}
	id := int64(0)
	if r.PathValue("id") != "" {
		var valid bool
		id, valid = pathID(w, r)
		if !valid {
			return
		}
		if !templates {
			if err := s.contractOwner(r, tx, uid, id); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		args = append(args, id)
		where += fmt.Sprintf(" AND c.id=$%d", len(args))
	}
	var total int64
	if err := tx.QueryRow(r.Context(), "SELECT count(*)"+where, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := tx.Query(r.Context(), fmt.Sprintf("SELECT %s%s ORDER BY c.id DESC LIMIT $%d OFFSET $%d", cols, where, len(args)-1, len(args)), args...)
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
	if id > 0 {
		if len(items) == 0 {
			s.fail(w, r, pgx.ErrNoRows)
			return
		}
		item := items[0]
		for _, table := range []string{"history", "documents"} {
			columns := "id,version,status,created_at"
			if table == "history" {
				columns = "id,action,before_status,after_status,note,version,created_at"
			}
			rows, err = tx.Query(r.Context(), "SELECT "+columns+" FROM contract_"+table+" WHERE contract_id=$1 ORDER BY id", id)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			item[table], err = pgx.CollectRows(rows, pgx.RowToMap)
			if err != nil {
				s.fail(w, r, err)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/document") {
			if err = tx.Commit(r.Context()); err != nil {
				s.fail(w, r, err)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="contract-%d.html"`, id))
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			partyJSON, _ := json.MarshalIndent(item["party_snapshot"], "", "  ")
			serviceJSON, _ := json.MarshalIndent(item["service_snapshot"], "", "  ")
			fmt.Fprintf(w, "<!doctype html><html lang=\"zh\"><meta charset=\"utf-8\"><title>%s</title><body><h1>%s</h1><pre style=\"white-space:pre-wrap\">%s</pre><p>姓名确认：%s</p><p>状态：%s</p><h2>双方信息（生成时冻结）</h2><pre>%s</pre><h2>服务信息（生成时冻结）</h2><pre>%s</pre></body></html>", html.EscapeString(fmt.Sprint(item["template_name"])), html.EscapeString(fmt.Sprint(item["template_name"])), html.EscapeString(fmt.Sprint(item["content"])), html.EscapeString(fmt.Sprint(item["signer_name"])), html.EscapeString(fmt.Sprint(item["status"])), html.EscapeString(string(partyJSON)), html.EscapeString(string(serviceJSON)))
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, item)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

type contractInput struct {
	ServiceID       int64  `json:"service_id"`
	TemplateID      int64  `json:"template_id"`
	Name            string `json:"name"`
	Content         string `json:"content"`
	Enabled         *bool  `json:"enabled"`
	ExpectedVersion int64  `json:"expected_version"`
	Decision        string `json:"decision"`
	Note            string `json:"note"`
	SignerName      string `json:"signer_name"`
	Accepted        bool   `json:"accepted"`
	Carrier         string `json:"carrier"`
	TrackingNumber  string `json:"tracking_number"`
	Recipient       string `json:"recipient"`
	Address         string `json:"address"`
	Phone           string `json:"phone"`
	Reason          string `json:"reason"`
	Items           []struct {
		ID              int64 `json:"id"`
		ExpectedVersion int64 `json:"expected_version"`
	} `json:"items"`
}

func (s *Server) contractWrite(w http.ResponseWriter, r *http.Request) {
	var fields map[string]json.RawMessage
	if !decode(w, r, &fields) {
		return
	}
	allowed := map[string]bool{}
	var names []string
	switch {
	case strings.Contains(r.URL.Path, "contract-templates"):
		names = []string{"name", "content", "enabled", "expected_version"}
	case strings.HasSuffix(r.URL.Path, "bulk-void"):
		names = []string{"items", "reason"}
	case strings.HasSuffix(r.URL.Path, "/submit"):
		names = []string{"signer_name", "accepted", "expected_version"}
	case strings.HasSuffix(r.URL.Path, "/review"):
		names = []string{"decision", "note", "expected_version"}
	case strings.HasSuffix(r.URL.Path, "/shipping"):
		names = []string{"carrier", "tracking_number", "recipient", "address", "phone", "expected_version"}
	case strings.HasSuffix(r.URL.Path, "/void"):
		names = []string{"reason", "expected_version"}
	default:
		names = []string{"service_id", "template_id"}
	}
	for _, name := range names {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			writeError(w, 400, "unknown_field", "请求字段不适用于该操作")
			return
		}
	}
	var in contractInput
	encoded, _ := json.Marshal(fields)
	if err := json.Unmarshal(encoded, &in); err != nil {
		writeError(w, 400, "invalid_contract_input", "请求资料无效")
		return
	}
	sort.Slice(in.Items, func(i, j int) bool { return in.Items[i].ID < in.Items[j].ID })
	for _, value := range []string{in.Name, in.Content, in.Note, in.SignerName, in.Carrier, in.TrackingNumber, in.Recipient, in.Address, in.Phone, in.Reason} {
		if strings.ContainsRune(value, 0) {
			writeError(w, 400, "invalid_contract_text", "文本不能含空字符")
			return
		}
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		writeError(w, 400, "idempotency_key_required", "请提供8至128字符幂等键")
		return
	}
	templates := strings.Contains(r.URL.Path, "contract-templates")
	tx, uid, ok := s.contractTx(w, r, true, templates)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	fail := func(err error) { s.fail(w, r, err) }
	op := r.Method + " " + r.URL.Path
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if _, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("contract:%d:%s:%s", uid, op, key)); err != nil {
		fail(err)
		return
	}
	id := int64(0)
	if r.PathValue("id") != "" {
		id, ok = pathID(w, r)
		if !ok {
			return
		}
		if !templates {
			if err := s.contractOwner(r, tx, uid, id); err != nil {
				fail(err)
				return
			}
		}
	}
	for _, item := range in.Items {
		if err := s.contractOwner(r, tx, uid, item.ID); err != nil {
			fail(err)
			return
		}
	}
	if !templates && id == 0 && !strings.HasSuffix(r.URL.Path, "bulk-void") {
		var owner int64
		if err := tx.QueryRow(r.Context(), "SELECT user_id FROM services WHERE id=$1", in.ServiceID).Scan(&owner); err != nil {
			fail(err)
			return
		}
		if adminRoute(r) {
			if err := store.LockAdminCustomer(r.Context(), tx, uid, owner); err != nil {
				fail(err)
				return
			}
		} else if owner != uid {
			fail(pgx.ErrNoRows)
			return
		}
	}
	var saved string
	var result map[string]any
	err := tx.QueryRow(r.Context(), "SELECT request_hash,result FROM contract_receipts WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3", uid, op, key).Scan(&saved, &result)
	if err == nil {
		if saved != hash {
			writeError(w, 409, "idempotency_conflict", "幂等键内容冲突")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			fail(err)
			return
		}
		write(w, 200, result)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		fail(err)
		return
	}
	if templates {
		if strings.TrimSpace(in.Name) == "" || len(in.Name) > 255 || in.Enabled == nil {
			writeError(w, 400, "invalid_template", "模板名称与开关必填")
			return
		}
		if _, err = store.RenderContract(in.Content, store.ContractValues()); err != nil {
			fail(err)
			return
		}
		if id == 0 {
			err = tx.QueryRow(r.Context(), "INSERT INTO contract_templates(name,content,enabled) VALUES($1,$2,$3) RETURNING id", in.Name, in.Content, *in.Enabled).Scan(&id)
		} else {
			var version int64
			err = tx.QueryRow(r.Context(), "SELECT version FROM contract_templates WHERE id=$1 FOR UPDATE", id).Scan(&version)
			if err == nil && version != in.ExpectedVersion {
				writeError(w, 409, "version_conflict", "模板版本已变更")
				return
			}
			if err == nil {
				_, err = store.UpdateVersioned(r.Context(), tx, "contract_templates", `name=$1,content=$2,enabled=$3`, id, in.ExpectedVersion, in.Name, in.Content, *in.Enabled)
			}
		}
		if err != nil {
			fail(err)
			return
		}
		if err = store.Audit(r.Context(), tx, uid, "contract.template_saved", "contract_template", id, map[string]any{"id": id}); err != nil {
			fail(err)
			return
		}
		rows, e := tx.Query(r.Context(), "SELECT * FROM contract_templates WHERE id=$1", id)
		if e != nil {
			fail(e)
			return
		}
		result, err = pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	} else if id == 0 && !strings.HasSuffix(r.URL.Path, "bulk-void") {
		result, err = s.contractCreate(r, tx, uid, in)
	} else {
		if id > 0 {
			in.Items = append(in.Items, struct {
				ID              int64 `json:"id"`
				ExpectedVersion int64 `json:"expected_version"`
			}{id, in.ExpectedVersion})
		}
		if len(in.Items) == 0 || len(in.Items) > 50 {
			writeError(w, 400, "invalid_contract_items", "请选择1至50项")
			return
		}
		seen := map[int64]bool{}
		for _, item := range in.Items {
			if seen[item.ID] {
				writeError(w, 400, "duplicate_contract", "合同重复")
				return
			}
			seen[item.ID] = true
			result, err = s.contractTransition(r, tx, uid, item.ID, item.ExpectedVersion, in)
			if err != nil {
				break
			}
		}
		if len(in.Items) > 1 {
			result = map[string]any{"count": len(in.Items)}
		}
	}
	if err != nil {
		fail(err)
		return
	}
	raw, _ = json.Marshal(result)
	_, err = tx.Exec(r.Context(), "INSERT INTO contract_receipts(actor_id,operation,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,$5)", uid, op, key, hash, raw)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(err)
		return
	}
	write(w, 200, result)
}
func (s *Server) contractEnabled(r *http.Request, tx pgx.Tx) error {
	var enabled bool
	if err := tx.QueryRow(r.Context(), "SELECT enabled FROM contract_settings WHERE id=1 FOR SHARE").Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return &store.Conflict{Code: "contracts_disabled", Message: "合同功能已关闭"}
	}
	return nil
}
func (s *Server) contractCreate(r *http.Request, tx pgx.Tx, uid int64, in contractInput) (map[string]any, error) {
	if err := s.contractEnabled(r, tx); err != nil {
		return nil, err
	}
	var owner int64
	if err := tx.QueryRow(r.Context(), "SELECT user_id FROM services WHERE id=$1", in.ServiceID).Scan(&owner); err != nil {
		return nil, err
	}
	if adminRoute(r) {
		if err := store.LockAdminCustomer(r.Context(), tx, uid, owner); err != nil {
			return nil, err
		}
	} else if owner != uid {
		return nil, pgx.ErrNoRows
	}
	rows, err := tx.Query(r.Context(), `SELECT s.id,s.user_id,u.display_name AS customer_name,u.email AS customer_email,oi.product_name,s.name AS service_name,s.domain,s.status,i.paid_at FROM services s JOIN users u ON u.id=s.user_id JOIN order_items oi ON oi.id=s.order_item_id JOIN invoices i ON i.order_id=s.order_id WHERE s.id=$1 AND s.status IN('active','suspended') AND i.status='paid' AND i.paid_at IS NOT NULL FOR SHARE OF s,u,oi,i`, in.ServiceID)
	if err != nil {
		return nil, err
	}
	snapshot, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	var name, content string
	if err = tx.QueryRow(r.Context(), "SELECT name,content FROM contract_templates WHERE id=$1 AND enabled FOR SHARE", in.TemplateID).Scan(&name, &content); err != nil {
		return nil, err
	}
	values := store.ContractValues()
	for key := range values {
		values[key] = fmt.Sprint(snapshot[key])
	}
	content, err = store.RenderContract(content, values)
	if err != nil {
		return nil, err
	}
	party := map[string]any{"customer_name": snapshot["customer_name"], "customer_email": snapshot["customer_email"]}
	var provider map[string]any
	if err = tx.QueryRow(r.Context(), "SELECT jsonb_build_object('site_name',site_name,'company_name',company_name,'address',address,'support_email',support_email,'support_phone',support_phone) FROM site_settings WHERE id=1 FOR SHARE").Scan(&provider); err != nil {
		return nil, err
	}
	party["provider"] = provider
	partyRaw, _ := json.Marshal(party)
	serviceRaw, _ := json.Marshal(snapshot)
	var id int64
	err = tx.QueryRow(r.Context(), `INSERT INTO contracts(customer_id,service_id,template_id,template_name,customer_name,customer_email,product_name,service_name,domain,paid_at,content,party_snapshot,service_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, owner, in.ServiceID, in.TemplateID, name, snapshot["customer_name"], snapshot["customer_email"], snapshot["product_name"], snapshot["service_name"], snapshot["domain"], snapshot["paid_at"], content, partyRaw, serviceRaw).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "contracts_service_live" {
			return nil, &store.Conflict{Code: "contract_exists", Message: "该服务已有有效合同"}
		}
		return nil, err
	}
	if err = store.ContractEvent(r.Context(), tx, id, uid, 1, "created", "", "draft", ""); err != nil {
		return nil, err
	}
	return contractResult(r, tx, id)
}
func contractResult(r *http.Request, tx pgx.Tx, id int64) (map[string]any, error) {
	rows, err := tx.Query(r.Context(), "SELECT * FROM contracts WHERE id=$1", id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
}
func (s *Server) contractTransition(r *http.Request, tx pgx.Tx, uid, id, expected int64, in contractInput) (map[string]any, error) {
	if err := s.contractOwner(r, tx, uid, id); err != nil {
		return nil, err
	}
	if strings.HasSuffix(r.URL.Path, "/submit") {
		if err := s.contractEnabled(r, tx); err != nil {
			return nil, err
		}
		var serviceID int64
		if err := tx.QueryRow(r.Context(), "SELECT service_id FROM contracts WHERE id=$1", id).Scan(&serviceID); err != nil {
			return nil, err
		}
		var eligible int64
		if err := tx.QueryRow(r.Context(), `SELECT s.id FROM services s JOIN invoices i ON i.order_id=s.order_id WHERE s.id=$1 AND s.user_id=$2 AND s.status IN('active','suspended') AND i.status='paid' AND i.paid_at IS NOT NULL FOR SHARE OF s,i`, serviceID, uid).Scan(&eligible); err != nil {
			return nil, err
		}
	}
	var before string
	var version int64
	if err := tx.QueryRow(r.Context(), "SELECT status,version FROM contracts WHERE id=$1 FOR UPDATE", id).Scan(&before, &version); err != nil {
		return nil, err
	}
	if expected < 1 || version != expected {
		return nil, &store.Conflict{Code: "version_conflict", Message: "合同版本已变更"}
	}
	action, after := "void", "void"
	note := strings.TrimSpace(in.Reason)
	invalid := func() (map[string]any, error) {
		return nil, &store.Conflict{Code: "invalid_contract_transition", Message: "合同状态或提交资料无效"}
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/submit"):
		action, after = "submitted", "pending"
		if adminRoute(r) || !in.Accepted || strings.TrimSpace(in.SignerName) == "" || len(in.SignerName) > 120 || (before != "draft" && before != "rejected") {
			return invalid()
		}
		if err := s.contractEnabled(r, tx); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(r.Context(), "UPDATE contracts SET signer_name=$2,review_note='' WHERE id=$1", id, strings.TrimSpace(in.SignerName)); err != nil {
			return nil, err
		}
	case strings.HasSuffix(r.URL.Path, "/review"):
		action = "reviewed"
		note = strings.TrimSpace(in.Note)
		if !adminRoute(r) || before != "pending" || (in.Decision != "approve" && in.Decision != "reject") || (in.Decision == "reject" && note == "") {
			return invalid()
		}
		after = "approved"
		if in.Decision == "reject" {
			after = "rejected"
		}
		if _, err := tx.Exec(r.Context(), "UPDATE contracts SET review_note=$2 WHERE id=$1", id, note); err != nil {
			return nil, err
		}
	case strings.HasSuffix(r.URL.Path, "/shipping"):
		action, after = "shipping_saved", "shipped"
		if !adminRoute(r) || (before != "approved" && before != "shipped") || strings.TrimSpace(in.Carrier) == "" || strings.TrimSpace(in.TrackingNumber) == "" || strings.TrimSpace(in.Recipient) == "" || strings.TrimSpace(in.Address) == "" || strings.TrimSpace(in.Phone) == "" {
			return invalid()
		}
		if len(in.Carrier) > 100 || len(in.TrackingNumber) > 150 || len(in.Recipient) > 120 || len(in.Address) > 1000 || len(in.Phone) > 64 {
			return invalid()
		}
		if _, err := tx.Exec(r.Context(), "UPDATE contracts SET carrier=$2,tracking_number=$3,recipient=$4,address=$5,phone=$6 WHERE id=$1", id, in.Carrier, in.TrackingNumber, in.Recipient, in.Address, in.Phone); err != nil {
			return nil, err
		}
	default:
		if !adminRoute(r) || before == "void" || note == "" {
			return invalid()
		}
	}
	if len(note) > 2000 || strings.ContainsRune(note+in.SignerName+in.Address, 0) {
		return invalid()
	}
	if _, err := tx.Exec(r.Context(), "UPDATE contracts SET status=$2,version=version+1,updated_at=now() WHERE id=$1", id, after); err != nil {
		return nil, err
	}
	if err := store.ContractEvent(r.Context(), tx, id, uid, version+1, action, before, after, note); err != nil {
		return nil, err
	}
	return contractResult(r, tx, id)
}
