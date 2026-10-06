package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func customerFileUpload(h http.Handler, method, path, cookie, name string, payload []byte, in store.CustomerFileInput) *httptest.ResponseRecorder {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, _ := json.Marshal(in)
	writer.WriteField("metadata", string(metadata))
	if name != "" {
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			panic(err)
		}
		part.Write(payload)
	}
	writer.Close()
	r := httptest.NewRequest(method, path, &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Cookie", cookie)
	r.Header.Set("Origin", "http://localhost:3088")
	r.Header.Set("X-Requested-With", "zjmfgo")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func customerFileID(t *testing.T, h http.Handler, cookie string, in store.CustomerFileInput) int64 {
	t.Helper()
	w := customerFileUpload(h, "POST", "/api/v1/admin/customer-files", cookie, "客户文件.txt", []byte("客户专属文件内容\n"), in)
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func customerFilesData(t *testing.T, h http.Handler, path, cookie string) map[string]any {
	t.Helper()
	w := request(h, "GET", path, nil, cookie, "")
	expect(t, w, 200)
	return data(t, w)
}
func customerFilePath(id int64) string { return fmt.Sprintf("/api/v1/admin/customer-files/%d", id) }
func ownFilePath(id int64) string      { return fmt.Sprintf("/api/v1/customer-files/%d/file", id) }
func TestCustomerFilesLifecycleOwnershipAndByteIntegrity(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cf-root", "super_admin")
	first, customer := user(t, db, h, "cf-first", "customer")
	second, other := user(t, db, h, "cf-second", "customer")
	in := store.CustomerFileInput{CustomerID: first, Title: "专属指南", Remarks: "客户可见备注"}
	id := customerFileID(t, h, root, in)
	path := customerFilePath(id)
	public := customerFilesData(t, h, "/api/v1/customer-files", customer)
	if public["total"] != float64(1) {
		t.Fatal("own list missing")
	}
	item := public["items"].([]any)[0].(map[string]any)
	for _, key := range []string{"content", "uploader_id", "uploader_name", "version", "customer_name", "customer_username"} {
		if _, ok := item[key]; ok {
			t.Fatal("customer leaked", key)
		}
	}
	expect(t, request(h, "GET", ownFilePath(id), nil, "", ""), 401)
	expect(t, request(h, "GET", ownFilePath(id), nil, other, ""), 404)
	if customerFilesData(t, h, "/api/v1/customer-files", other)["total"] != float64(0) {
		t.Fatal("other list leaked")
	}
	if customerFilesData(t, h, fmt.Sprintf("/api/v1/customer-files?customer_id=%d", first), other)["total"] != float64(0) {
		t.Fatal("query overrode owner")
	}
	got := request(h, "GET", ownFilePath(id), nil, customer, "")
	expect(t, got, 200)
	digest := sha256.Sum256(got.Body.Bytes())
	if got.Body.String() != "客户专属文件内容\n" || item["sha256"] != hex.EncodeToString(digest[:]) {
		t.Fatal("bytes/hash mismatch")
	}
	if !strings.Contains(got.Header().Get("Content-Disposition"), "filename*=UTF-8''") || got.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("download headers")
	}
	head := request(h, "HEAD", ownFilePath(id), nil, customer, "")
	expect(t, head, 200)
	if head.Body.Len() != 0 {
		t.Fatal("HEAD bytes")
	}
	detail := customerFilesData(t, h, path, root)
	if detail["downloads"] != float64(1) || detail["version"] != float64(1) {
		t.Fatal("HEAD advanced counter/version")
	}
	in.ExpectedVersion = 1
	in.Title = "更新指南"
	expect(t, customerFileUpload(h, "PUT", path, root, "", nil, in), 200)
	expect(t, customerFileUpload(h, "PUT", path, root, "更新.txt", []byte("new"), in), 409)
	in.ExpectedVersion = 2
	expect(t, customerFileUpload(h, "PUT", path, root, "更新.txt", []byte("新版内容"), in), 200)
	in.ExpectedVersion = 3
	in.CustomerID = second
	expect(t, customerFileUpload(h, "PUT", path, root, "", nil, in), 200)
	expect(t, request(h, "GET", ownFilePath(id), nil, customer, ""), 404)
	got = request(h, "GET", ownFilePath(id), nil, other, "")
	expect(t, got, 200)
	if got.Body.String() != "新版内容" {
		t.Fatal("move lost bytes")
	}
	detail = customerFilesData(t, h, path, root)
	if detail["version"] != float64(4) || detail["downloads"] != float64(2) || detail["customer_id"] != float64(second) {
		t.Fatal("move metadata", detail)
	}
	if customerFilesData(t, h, "/api/v1/customer-files", customer)["total"] != float64(0) {
		t.Fatal("previous owner still listed")
	}
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 3}, root, ""), 409)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 4}, root, ""), 200)
	expect(t, request(h, "GET", ownFilePath(id), nil, other, ""), 404)
	var auditCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_type='customer_file' AND entity_id=$1", fmt.Sprint(id)).Scan(&auditCount); err != nil || auditCount != 7 {
		t.Fatal("lifecycle audit", auditCount, err)
	}
}
func TestCustomerFilesAdministratorSalesScopeAndBulkAtomicity(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cf-scope-root", "super_admin")
	staffID, staff := user(t, db, h, "cf-scope-staff", "admin")
	otherID, otherStaff := user(t, db, h, "cf-scope-other", "admin")
	readerID, reader := user(t, db, h, "cf-reader", "admin")
	own, customer := user(t, db, h, "cf-own", "customer")
	second, _ := user(t, db, h, "cf-own-second", "customer")
	foreign, _ := user(t, db, h, "cf-foreign", "customer")
	unassigned, _ := user(t, db, h, "cf-unassigned", "customer")
	contentRole(t, db, staffID, "customer_files.view", "customer_files.manage")
	contentRole(t, db, readerID, "customer_files.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned' WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []int64{own, second} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", c, staffID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", foreign, otherID); err != nil {
		t.Fatal(err)
	}
	ids := map[int64]int64{}
	for _, c := range []int64{own, second, foreign, unassigned} {
		ids[c] = customerFileID(t, h, root, store.CustomerFileInput{CustomerID: c, Title: fmt.Sprintf("客户-%d", c)})
	}
	list := customerFilesData(t, h, "/api/v1/admin/customer-files", staff)
	if list["total"] != float64(2) {
		t.Fatal("scope count", list)
	}
	owners := listData(t, request(h, "GET", "/api/v1/admin/customer-files/owners", nil, staff, ""))
	if len(owners) != 2 {
		t.Fatal("owner directory scope")
	}
	for _, c := range []int64{foreign, unassigned} {
		id := ids[c]
		expect(t, request(h, "GET", customerFilePath(id), nil, staff, ""), 404)
		expect(t, request(h, "GET", customerFilePath(id)+"/file", nil, staff, ""), 404)
		expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customer-files?customer_id=%d", c), nil, staff, ""), 404)
		expect(t, customerFileUpload(h, "POST", "/api/v1/admin/customer-files", staff, "scope.txt", []byte("text"), store.CustomerFileInput{CustomerID: c, Title: "denied"}), 404)
	}
	path := customerFilePath(ids[own])
	input := store.CustomerFileInput{CustomerID: foreign, Title: "forbidden move", ExpectedVersion: 1}
	expect(t, customerFileUpload(h, "PUT", path, staff, "", nil, input), 404)
	input.CustomerID = own
	expect(t, customerFileUpload(h, "PUT", customerFilePath(ids[foreign]), staff, "", nil, input), 404)
	input.CustomerID = second
	expect(t, customerFileUpload(h, "PUT", path, staff, "", nil, input), 200)
	expect(t, request(h, "GET", ownFilePath(ids[own]), nil, customer, ""), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='all' WHERE id=$1", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, reader, ""), 200)
	expect(t, request(h, "GET", path+"/file", nil, reader, ""), 200)
	expect(t, customerFileUpload(h, "PUT", path, reader, "", nil, input), 403)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 2}, reader, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/customer-files", nil, customer, ""), 403)
	// Atomic batch must not partially delete an authorized file when another item is out of scope.
	bulk := map[string]any{"items": []store.CustomerFileVersion{{ID: ids[second], ExpectedVersion: 1}, {ID: ids[foreign], ExpectedVersion: 1}}}
	expect(t, request(h, "POST", "/api/v1/admin/customer-files/bulk-delete", bulk, staff, ""), 404)
	expect(t, request(h, "GET", customerFilePath(ids[second]), nil, staff, ""), 200)
	bulk["items"] = []store.CustomerFileVersion{{ID: ids[second], ExpectedVersion: 1}, {ID: ids[own], ExpectedVersion: 1}}
	expect(t, request(h, "POST", "/api/v1/admin/customer-files/bulk-delete", bulk, staff, ""), 409)
	expect(t, request(h, "GET", customerFilePath(ids[second]), nil, staff, ""), 200)
	bulk["items"] = []store.CustomerFileVersion{{ID: ids[own], ExpectedVersion: 2}, {ID: ids[second], ExpectedVersion: 2}}
	expect(t, request(h, "POST", "/api/v1/admin/customer-files/bulk-delete", bulk, staff, ""), 409)
	expect(t, request(h, "GET", customerFilePath(ids[own]), nil, staff, ""), 200)
	bulk["items"] = []store.CustomerFileVersion{{ID: ids[second], ExpectedVersion: 1}, {ID: ids[own], ExpectedVersion: 2}}
	expect(t, request(h, "POST", "/api/v1/admin/customer-files/bulk-delete", bulk, staff, ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_ownerless=true WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", customerFilePath(ids[unassigned]), nil, staff, ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='assigned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", customerFilePath(ids[foreign]), nil, staff, ""), 200)
	expect(t, request(h, "GET", customerFilePath(ids[unassigned]), nil, staff, ""), 404)
	// Real customer reassignment immediately revokes old staff scope.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',sales_enabled=true WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	// Assignment requires a sales administrator with customers.view.
	contentRole(t, db, staffID, "customers.view", "customer_files.view", "customer_files.manage")
	contentRole(t, db, otherID, "customers.view", "customer_files.view", "customer_files.manage")
	salesAssignment(t, h, root, foreign, staffID, 1)
	expect(t, request(h, "GET", customerFilePath(ids[foreign]), nil, staff, ""), 200)
	salesAssignment(t, h, root, foreign, otherID, 2)
	expect(t, request(h, "GET", customerFilePath(ids[foreign]), nil, staff, ""), 404)
	expect(t, request(h, "GET", customerFilePath(ids[foreign]), nil, otherStaff, ""), 200)
}
func TestCustomerFilesCredentialsSettingsAndValidation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cf-validation-root", "super_admin")
	uid, customer := user(t, db, h, "cf-validation-customer", "customer")
	in := store.CustomerFileInput{CustomerID: uid, Title: "private"}
	id := customerFileID(t, h, root, in)
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "private"}, customer, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", ownFilePath(id), nil, secret), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET expires_at=now()-interval '1 second' WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", ownFilePath(id), nil, secret), 401)
	expect(t, request(h, "POST", "/api/v1/admin/roles", roleInput("missing file view", "customer_files.manage"), root, ""), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", ownFilePath(id), nil, customer, ""), 401)
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", map[string]any{"enabled": false, "expected_version": 1}, root, ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='active' WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/customer-files", nil, customer, ""), 403)
	expect(t, request(h, "GET", ownFilePath(id), nil, customer, ""), 403)
	expect(t, request(h, "GET", customerFilePath(id)+"/file", nil, root, ""), 200)
	expect(t, request(h, "PUT", "/api/v1/admin/download-settings", map[string]any{"enabled": true, "expected_version": 2}, root, ""), 200)
	for _, name := range []string{"../file.txt", "file\\file.txt", "file.php", "file.png"} {
		expect(t, customerFileUpload(h, "POST", "/api/v1/admin/customer-files", root, name, []byte("text"), in), 400)
	}
	expect(t, customerFileUpload(h, "POST", "/api/v1/admin/customer-files", root, "", nil, in), 409)
	expect(t, customerFileUpload(h, "POST", "/api/v1/admin/customer-files", root, "empty.txt", nil, in), 400)
	for _, owner := range []int64{0, -1, 999999} {
		in.CustomerID = owner
		want := 409
		if owner == 999999 {
			want = 404
		}
		expect(t, customerFileUpload(h, "POST", "/api/v1/admin/customer-files", root, "x.txt", []byte("text"), in), want)
	}
	in.CustomerID = uid
	in.Title = strings.Repeat("🙂", 255)
	customerFileID(t, h, root, in)
	in.Title += "🙂"
	expect(t, customerFileUpload(h, "POST", "/api/v1/admin/customer-files", root, "x.txt", []byte("text"), in), 409)
	expect(t, request(h, "POST", "/api/v1/admin/customer-files/bulk-delete", map[string]any{"items": []store.CustomerFileVersion{{ID: id, ExpectedVersion: 1}, {ID: id, ExpectedVersion: 1}}}, root, ""), 409)
	expect(t, request(h, "GET", "/api/v1/admin/customer-files?customer_id=-1", nil, root, ""), 400)
}
func TestCustomerFilesConcurrentEditsAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cf-race-root", "super_admin")
	uid, customer := user(t, db, h, "cf-race-customer", "customer")
	other, _ := user(t, db, h, "cf-race-other", "customer")
	in := store.CustomerFileInput{CustomerID: uid, Title: "race"}
	id := customerFileID(t, h, root, in)
	second := customerFileID(t, h, root, in)
	path := customerFilePath(id)
	in.ExpectedVersion = 1
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			codes <- customerFileUpload(h, "PUT", path, root, "新版.txt", []byte("replacement"), in).Code
		})
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for c := range codes {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("race", counts)
	}
	for range 8 {
		wg.Go(func() {
			w := request(h, "GET", ownFilePath(id), nil, customer, "")
			if w.Code != 200 || w.Body.String() != "replacement" {
				t.Error("download race")
			}
		})
	}
	wg.Wait()
	before := customerFilesData(t, h, path, root)
	if before["downloads"] != float64(8) {
		t.Fatal("counter race")
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_cf_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.entity_type='customer_file' THEN RAISE EXCEPTION 'customer file audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cf BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_cf_audit();`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedVersion = 2
	in.CustomerID = other
	in.Title = "rollback"
	expect(t, customerFileUpload(h, "PUT", path, root, "bad.txt", []byte("bad"), in), 500)
	expect(t, request(h, "DELETE", path, map[string]int{"expected_version": 2}, root, ""), 500)
	expect(t, request(h, "POST", "/api/v1/admin/customer-files/bulk-delete", map[string]any{"items": []store.CustomerFileVersion{{ID: second, ExpectedVersion: 1}, {ID: id, ExpectedVersion: 2}}}, root, ""), 500)
	expect(t, request(h, "GET", ownFilePath(id), nil, customer, ""), 500)
	after := customerFilesData(t, h, path, root)
	for _, key := range []string{"customer_id", "sha256", "filename", "title", "version", "downloads"} {
		if after[key] != before[key] {
			t.Fatal("audit changed", key)
		}
	}
	expect(t, request(h, "GET", customerFilePath(second), nil, root, ""), 200)
}
func TestCustomerFilesWaitingRevocationAndSalesTransfer(t *testing.T) {
	for _, kind := range []string{"session", "api_key", "read_permission", "manage_permission", "sales_transfer", "sales_wait_session"} {
		t.Run(kind, func(t *testing.T) {
			db, h := testServer(t)
			_, root := user(t, db, h, "cf-wait-root", "super_admin")
			staffID, staff := user(t, db, h, "cf-wait-staff", "admin")
			uid, customer := user(t, db, h, "cf-wait-customer", "customer")
			contentRole(t, db, staffID, "customer_files.view", "customer_files.manage")
			in := store.CustomerFileInput{CustomerID: uid, Title: "wait"}
			id := customerFileID(t, h, root, in)
			path := customerFilePath(id)
			var secret string
			if kind == "api_key" {
				w := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "wait"}, customer, "")
				expect(t, w, 201)
				secret = data(t, w)["secret"].(string)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if kind == "sales_transfer" || kind == "sales_wait_session" {
				if _, err = tx.Exec(ctx, "UPDATE users SET customer_scope='owned' WHERE id=$1", staffID); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, "UPDATE users SET sales_owner_id=$2 WHERE id=$1", uid, staffID); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				tx, err = db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", uid)
			} else {
				_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108199)")
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				switch kind {
				case "session":
					done <- request(h, "GET", ownFilePath(id), nil, customer, "").Code
				case "api_key":
					done <- requestBearer(h, "GET", ownFilePath(id), nil, secret).Code
				case "read_permission", "sales_transfer", "sales_wait_session":
					done <- request(h, "GET", path+"/file", nil, staff, "").Code
				default:
					done <- customerFileUpload(h, "POST", "/api/v1/admin/customer-files", staff, "wait.txt", []byte("text"), in).Code
				}
			}()
			lockQuery := "SELECT pg_advisory_xact_lock(70108199)"
			if kind == "sales_transfer" || kind == "sales_wait_session" {
				lockQuery = "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))"
			}
			for {
				var waiting bool
				if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query=$1)", lockQuery).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("never waited")
				case <-time.After(10 * time.Millisecond):
				}
			}
			want := 401
			switch kind {
			case "session":
				_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", uid)
			case "api_key":
				_, err = tx.Exec(ctx, "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret))
			case "read_permission":
				_, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
				want = 403
			case "manage_permission":
				_, err = tx.Exec(ctx, "UPDATE admin_roles SET permissions=ARRAY['customer_files.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
				want = 403
			case "sales_transfer":
				_, err = tx.Exec(ctx, "UPDATE users SET sales_owner_id=NULL WHERE id=$1", uid)
				want = 404
			case "sales_wait_session":
				_, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", staffID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case code := <-done:
				if code != want {
					t.Fatal("revocation", code, want)
				}
			case <-ctx.Done():
				t.Fatal("stuck")
			}
			var count, sum int
			if err = db.Pool.QueryRow(ctx, "SELECT count(*),sum(downloads) FROM customer_files").Scan(&count, &sum); err != nil || count != 1 || sum != 0 {
				t.Fatal("revocation persisted", err, count, sum)
			}
		})
	}
}
func TestCustomerFilesSearchPaginationAndScopeCounts(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cf-page-root", "super_admin")
	uid, customer := user(t, db, h, "cf-page-customer", "customer")
	other, second := user(t, db, h, "cf-page-other", "customer")
	for i := range 23 {
		customerFileID(t, h, root, store.CustomerFileInput{CustomerID: uid, Title: fmt.Sprintf("指南 %02d", i), Remarks: "分页搜索"})
	}
	customerFileID(t, h, root, store.CustomerFileInput{CustomerID: other, Title: "private other"})
	for offset, want := range map[int]int{0: 20, 20: 3, 40: 0} {
		body := customerFilesData(t, h, fmt.Sprintf("/api/v1/customer-files?limit=20&offset=%d", offset), customer)
		if body["total"] != float64(23) || len(body["items"].([]any)) != want {
			t.Fatal("page", offset)
		}
	}
	if customerFilesData(t, h, "/api/v1/customer-files?q=00", customer)["total"] != float64(1) || customerFilesData(t, h, "/api/v1/customer-files?q=private", customer)["total"] != float64(0) || customerFilesData(t, h, "/api/v1/customer-files", second)["total"] != float64(1) {
		t.Fatal("search owner")
	}
}
func TestCustomerFileBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "customer-file-super", func(db *store.Store, h http.Handler) {
		_, root := user(t, db, h, "cf-browser-root", "super_admin")
		uid, _ := user(t, db, h, "file-customer-a", "customer")
		user(t, db, h, "file-customer-b", "customer")
		viewerID, _ := user(t, db, h, "file-reader", "admin")
		contentRole(t, db, viewerID, "customer_files.view")
		customerFileID(t, h, root, store.CustomerFileInput{CustomerID: uid, Title: "初始专属资料", Remarks: "验收文件"})
	})
}
