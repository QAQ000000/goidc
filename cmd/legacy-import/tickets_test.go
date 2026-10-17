package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/httpapi"
	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func ticketImportFixture(t *testing.T) (legacy.Snapshot, string) {
	t.Helper()
	s := importFixture()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "common/ticket"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"uuid^客户.txt": "正文附件字节\n", "merged.txt": "合并附件字节", "reply.txt": "客户回复附件", "staff.txt": "管理员公开回复附件", "private.txt": "仅管理员内部附件"} {
		if err := os.WriteFile(filepath.Join(root, "common/ticket", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s["staff"] = []legacy.Record{{"id": "10", "user_login": "old-support", "user_nickname": "历史客服", "user_status": "1", "create_time": "1700000000"}}
	dept := func(id, hidden string) legacy.Record {
		return legacy.Record{"id": id, "name": strings.Repeat("部门", 70), "description": "原说明", "order": "3", "hidden": hidden, "only_reg_client": "0", "only_client_open": "0", "no_auto_reply": "0", "is_product_order": "0", "is_related_upstream": "0", "is_certifi": "0"}
	}
	s["ticket_departments"] = []legacy.Record{dept("71", "0"), dept("72", "1")}
	for id, title := range []string{"待处理", "已回复", "客户回复", "关闭", "处理中"} {
		s["ticket_statuses"] = append(s["ticket_statuses"], legacy.Record{"id": strconv.Itoa(id + 1), "title": title, "order": "1", "show_active": "1", "show_await": "1", "auto_close": "1"})
	}
	ticket := func(id, uid, department, merged, attachment string) legacy.Record {
		return legacy.Record{"id": id, "tid": "原工单号-重复", "uid": uid, "dptid": department, "host_id": "0", "title": "旧工单" + id, "content": "原正文 <script>只显示文字</script>", "status": "3", "priority": "medium", "admin": "", "admin_id": "0", "attachment": attachment, "merged_ticket_id": merged, "handle": "0", "create_time": "1700000000", "update_time": "1700000300", "last_reply_time": "1700000200", "handle_time": "0", "client_unread": "1", "admin_unread": "0", "is_deliver": "0", "is_receive": "0", "upstream_tid": ""}
	}
	s["tickets"] = []legacy.Record{ticket("201", "10", "71", "0", "uuid^客户.txt"), ticket("202", "10", "71", "201", "merged.txt"), ticket("203", "11", "72", "0", "")}
	s["tickets"][0]["host_id"] = "40"
	s["tickets"][0]["handle"] = "10"
	s["ticket_replies"] = []legacy.Record{{"id": "211", "tid": "201", "uid": "10", "admin_id": "0", "contactid": "0", "content": "客户旧回复", "attachment": "reply.txt", "create_time": "1700000100", "editor": "plain"}, {"id": "212", "tid": "202", "uid": "0", "admin_id": "10", "contactid": "0", "content": "客服旧回复", "attachment": "staff.txt", "create_time": "1700000200", "editor": "markdown"}}
	s["ticket_notes"] = []legacy.Record{{"id": "221", "tid": "201", "admin": "old-support", "content": "内部机密备注", "attachment": "private.txt", "create_time": "1700000150", "editor": "plain"}}
	return s, root
}

func TestTicketImportIdentityLineageBytesAndReplay(t *testing.T) {
	db := importTestDB(t)
	admin := navigationAdmin(t, db)
	s, root := ticketImportFixture(t)
	im := fileImporter(s, root, admin)
	if err := im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	uid, staff := importedID(t, db, "users", 10), importedID(t, db, "staff", 10)
	if uid == staff {
		t.Fatal("staff and customer source IDs collided")
	}
	var disabled bool
	var name string
	if err := db.Pool.QueryRow(t.Context(), `SELECT u.display_name,u.status='disabled' AND u.password_reset_required AND NOT r.active AND cardinality(r.permissions)=0 FROM users u JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1`, staff).Scan(&name, &disabled); err != nil || name != "历史客服" || !disabled {
		t.Fatal("historical staff obtained access", err)
	}
	canonical, merged := importedID(t, db, "tickets", 201), importedID(t, db, "tickets", 202)
	var parent, owner, assignee, service int64
	var state, priority, original string
	if err := db.Pool.QueryRow(t.Context(), "SELECT merged_ticket_id,user_id,status FROM tickets WHERE id=$1", merged).Scan(&parent, &owner, &state); err != nil || parent != canonical || owner != uid || state != "closed" {
		t.Fatal("merge lineage", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT user_id,assignee_id,service_id,priority,legacy_ticket_no FROM tickets WHERE id=$1", canonical).Scan(&owner, &assignee, &service, &priority, &original); err != nil || owner != uid || assignee != staff || service != importedID(t, db, "services", 40) || priority != "normal" || original != "原工单号-重复" {
		t.Fatal("ticket mapping", err)
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(DISTINCT ticket_no) FROM tickets").Scan(&n); err != nil || n != 3 {
		t.Fatal("duplicate old number lost target", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT ticket_id,author_id FROM ticket_replies WHERE legacy_id=212").Scan(&parent, &owner); err != nil || parent != canonical || owner != staff {
		t.Fatal("reply identity or merge", err)
	}
	var private bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*),bool_or(note_id IS NOT NULL) FROM ticket_attachments WHERE ticket_id=$1", canonical).Scan(&n, &private); err != nil || n != 5 || !private {
		t.Fatal("attachment parents", err)
	}
	for _, r := range s["ticket_attachments"] {
		var content []byte
		var hash, filename string
		var stamp int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT content,sha256,filename,extract(epoch FROM created_at)::bigint FROM ticket_attachments WHERE legacy_id=$1", sourceID(r)).Scan(&content, &hash, &filename, &stamp); err != nil || hash != r["_file_sha256"] || hash != checksum(content) || filename != r["filename"] || stamp != referenceID(r, "create_time") {
			t.Fatal("attachment bytes/time/name", err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE tickets SET subject='现场修改',status='closed',version=7 WHERE id=$1", canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE ticket_departments SET name='现场部门',customer_visible=false,version=8 WHERE legacy_id=71"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE ticket_replies SET body='现场回复' WHERE legacy_id=211"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM legacy_id_map WHERE entity=ANY($1)", []string{"staff", "ticket_departments", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments"}); err != nil {
		t.Fatal(err)
	}
	replay := fileImporter(s, root, admin)
	if err := replay.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	for _, entity := range []string{"staff", "ticket_departments", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments"} {
		if replay.report.Replayed[entity] != len(s[entity]) {
			t.Fatal("incomplete replay", entity)
		}
	}
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT subject,status,version FROM tickets WHERE id=$1", canonical).Scan(&name, &state, &version); err != nil || name != "现场修改" || state != "closed" || version != 7 {
		t.Fatal("ticket edits overwritten", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT NOT customer_visible AND name='现场部门' AND version=8 FROM ticket_departments WHERE legacy_id=71").Scan(&private); err != nil || !private {
		t.Fatal("department edits overwritten", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT body FROM ticket_replies WHERE legacy_id=211").Scan(&name); err != nil || name != "现场回复" {
		t.Fatal("reply edit overwritten", err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM ticket_attachments WHERE legacy_id=1"); err != nil {
		t.Fatal(err)
	}
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "stale target mapping") {
		t.Fatal("deleted attachment recreated", err)
	}
}

func TestTicketImportValidationAndWholeBatchRollback(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(legacy.Snapshot)
	}{
		{"guest", func(s legacy.Snapshot) { s["tickets"][0]["uid"] = "0" }},
		{"foreign-service", func(s legacy.Snapshot) { s["tickets"][0]["uid"] = "11" }},
		{"foreign-reply", func(s legacy.Snapshot) { s["ticket_replies"][0]["uid"] = "11" }},
		{"contact", func(s legacy.Snapshot) { s["ticket_replies"][0]["contactid"] = "1" }},
		{"missing-staff", func(s legacy.Snapshot) { s["ticket_replies"][1]["admin_id"] = "99" }},
		{"missing-note-author", func(s legacy.Snapshot) { s["ticket_notes"][0]["admin"] = "missing" }},
		{"foreign-merge", func(s legacy.Snapshot) { s["tickets"][1]["merged_ticket_id"] = "203" }},
		{"merge-cycle", func(s legacy.Snapshot) { s["tickets"][0]["merged_ticket_id"] = "202" }},
		{"custom-state", func(s legacy.Snapshot) { s["tickets"][0]["status"] = "6" }},
		{"timestamp", func(s legacy.Snapshot) { s["ticket_replies"][0]["create_time"] = "invalid" }},
		{"eligibility", func(s legacy.Snapshot) { s["ticket_departments"][0]["is_certifi"] = "1" }},
		{"path", func(s legacy.Snapshot) { s["ticket_notes"][0]["attachment"] = "../private.txt" }},
		{"dangerous", func(s legacy.Snapshot) { s["tickets"][0]["attachment"] = "file.php" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, root := ticketImportFixture(t)
			c.edit(s)
			im := fileImporter(s, root, 1)
			err := legacy.ExpandTicketAttachments(s)
			if err == nil {
				err = im.validate()
			}
			if err == nil {
				err = im.prepareTicketFiles()
			}
			if err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	for _, table := range []string{"ticket_replies", "ticket_notes", "ticket_attachments", "audit_events"} {
		t.Run("rollback-"+table, func(t *testing.T) {
			db := importTestDB(t)
			admin := navigationAdmin(t, db)
			s, root := ticketImportFixture(t)
			if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION fail_ticket_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected ticket import failure'; END $$; CREATE TRIGGER fail_import BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_ticket_import()`); err != nil {
				t.Fatal(err)
			}
			if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil {
				t.Fatal("injected failure ignored")
			}
			var n int
			for _, target := range []string{"products", "orders", "services", "invoices", "ticket_departments", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments", "legacy_id_map", "legacy_source_records", "legacy_snapshot_manifests", "admin_roles"} {
				if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+target).Scan(&n); err != nil || n != 0 {
					t.Fatal("partial batch", target, n, err)
				}
			}
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&n); err != nil || n != 1 {
				t.Fatal("staff or customer rollback", err)
			}
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_import_runs WHERE status='failed'").Scan(&n); err != nil || n != 1 {
				t.Fatal("failed run lost", err)
			}
		})
	}
}

func TestImportedTicketAPIPrivacyVisibilityAndLiveOperations(t *testing.T) {
	db := importTestDB(t)
	admin := navigationAdmin(t, db)
	s, root := ticketImportFixture(t)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	uid, other, staff := importedID(t, db, "users", 10), importedID(t, db, "users", 11), importedID(t, db, "staff", 10)
	hash, err := bcrypt.GenerateFromPassword([]byte("ticket-api-fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET password_hash=$1,password_reset_required=false,status='active' WHERE id=ANY($2)", string(hash), []int64{uid, other, admin}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET password_hash=$1 WHERE id=$2", string(hash), staff); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(db, config.Config{PublicOrigin: "http://localhost:3088"}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	call := func(method, path string, body any, cookie string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://localhost:3088/api/v1"+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost:3088")
		r.Header.Set("X-Requested-With", "zjmfgo")
		r.Header.Set("Cookie", cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	login := func(name string) string {
		t.Helper()
		var email string
		if err := db.Pool.QueryRow(t.Context(), "SELECT email FROM users WHERE username=$1", name).Scan(&email); err != nil {
			t.Fatal(err)
		}
		w := call("POST", "/auth/login", map[string]string{"identifier": email, "password": "ticket-api-fixture"}, "")
		if w.Code != 200 {
			t.Fatal("login", w.Code)
		}
		c := w.Result().Cookies()[0]
		return c.Name + "=" + c.Value
	}
	owner, foreign, super := login("legacy-10"), login("legacy-11"), login("nav-import-root")
	var lockedName string
	if err = db.Pool.QueryRow(t.Context(), "SELECT username FROM users WHERE id=$1", staff).Scan(&lockedName); err != nil {
		t.Fatal(err)
	}
	locked := call("POST", "/auth/login", map[string]string{"username": lockedName, "password": "ticket-api-fixture"}, "")
	if locked.Code == 200 {
		t.Fatal("historical staff can log in")
	}
	id := importedID(t, db, "tickets", 201)
	path := fmt.Sprintf("/tickets/%d", id)
	check := func(method, p string, body any, cookie string, code int) *httptest.ResponseRecorder {
		t.Helper()
		w := call(method, p, body, cookie)
		if w.Code != code {
			t.Fatal("API", method, p, w.Code, code)
		}
		return w
	}
	unpack := func(w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var v struct{ Data map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Data
	}
	own := unpack(check("GET", path, nil, owner, 200))
	if own["legacy_ticket_no"] != "原工单号-重复" || own["legacy_status_label"] != "客户回复" || len(own["attachments"].([]any)) != 4 {
		t.Fatal("imported detail metadata")
	}
	if _, ok := own["notes"]; ok {
		t.Fatal("customer received notes")
	}
	check("GET", path, nil, foreign, 404)
	check("GET", fmt.Sprintf("/tickets/%d", importedID(t, db, "tickets", 202)), nil, owner, 409)
	privileged := unpack(check("GET", path, nil, super, 200))
	if len(privileged["notes"].([]any)) != 1 || len(privileged["attachments"].([]any)) != 5 {
		t.Fatal("admin note attachments missing")
	}
	var privateID, publicID int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM ticket_attachments WHERE note_id IS NOT NULL").Scan(&privateID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM ticket_attachments WHERE filename='客户.txt'").Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	file := func(a int64) string { return fmt.Sprintf("%s/attachments/%d", path, a) }
	if w := check("GET", file(publicID), nil, owner, 200); w.Body.String() != "正文附件字节\n" {
		t.Fatal("customer bytes")
	}
	check("GET", file(privateID), nil, owner, 404)
	check("DELETE", file(privateID), nil, owner, 404)
	check("GET", file(publicID), nil, foreign, 404)
	if w := check("GET", file(privateID), nil, super, 200); w.Body.String() != "仅管理员内部附件" {
		t.Fatal("private bytes")
	}
	department := importedID(t, db, "ticket_departments", 71)
	update := map[string]any{"name": strings.Repeat("部门", 70), "active": true, "customer_visible": false, "sort_order": 3, "expected_version": 1}
	check("PATCH", fmt.Sprintf("/admin/ticket-departments/%d", department), update, super, 200)
	check("GET", path, nil, owner, 404)
	check("GET", file(publicID), nil, owner, 404)
	check("DELETE", file(publicID), nil, owner, 404)
	check("POST", path+"/replies", map[string]any{"body": "不能回复隐藏工单", "version": 1}, owner, 404)
	hidden := check("GET", "/tickets", nil, owner, 200)
	if strings.Contains(hidden.Body.String(), "旧工单201") {
		t.Fatal("hidden ticket listed")
	}
	check("GET", path, nil, super, 200)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	check("GET", path, nil, owner, 404) // Replay preserves live hiding.
	update["customer_visible"] = true
	update["expected_version"] = 2
	check("PATCH", fmt.Sprintf("/admin/ticket-departments/%d", department), update, super, 200)
	check("POST", path+"/replies", map[string]any{"body": "迁移后的真实回复", "version": 1}, owner, 200)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	own = unpack(check("GET", path, nil, owner, 200))
	if own["version"] != float64(2) || len(own["replies"].([]any)) != 3 {
		t.Fatal("live reply overwritten")
	}
	check("PATCH", path, map[string]any{"version": 2}, owner, 200)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	own = unpack(check("GET", path, nil, owner, 200))
	if own["status"] != "closed" {
		t.Fatal("closure overwritten")
	}
	check("DELETE", file(privateID), nil, super, 200)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err == nil || !strings.Contains(err.Error(), "stale target mapping") {
		t.Fatal("removed private attachment recreated", err)
	}
}

func reconcileTicketAttachments(t *testing.T, db *store.Store, s legacy.Snapshot, root string) {
	t.Helper()
	for _, r := range s["ticket_attachments"] {
		b, mt, err := legacy.ReadTicketFile(root, r["stored_name"], r["filename"])
		if err != nil {
			t.Fatal(err)
		}
		var content []byte
		var hash, name, mime string
		var size int64
		if err = db.Pool.QueryRow(t.Context(), "SELECT content,sha256,filename,mime_type,size_bytes FROM ticket_attachments WHERE legacy_id=$1", sourceID(r)).Scan(&content, &hash, &name, &mime, &size); err != nil || !bytes.Equal(b, content) || hash != checksum(b) || name != r["filename"] || mime != mt || size != int64(len(b)) {
			t.Fatal("real source attachment reconciliation", err)
		}
	}
}
