package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/filestorage"
)

func TestFileStorageDownloadWaitingReadRechecksProductEntitlement(t *testing.T) {
	profile := filestorage.Profile{ID: "entitlements", Implementation: "local", Values: map[string]any{"root": t.TempDir()}}
	db, h := testServerConfig(t, func(cfg *config.Config) {
		cfg.FileStorage = fileStorageManager(t, profile.ID, profile)
	})
	_, root := user(t, db, h, "storage-entitlement-root", "super_admin")
	uid, customer := user(t, db, h, "storage-entitlement-customer", "customer")
	pid := product(t, db, 10)
	sid := downloadService(t, db, h, uid, customer, pid, "storage-entitlement-order")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	in := downloadInput()
	in.AccessLevel, in.ProductIDs = "product", []int64{pid}
	fid := downloadID(t, h, root, in)
	var objectID string
	var allowed bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT storage_object_id,download_allowed(id,$2) FROM downloads WHERE id=$1", fid, uid).Scan(&objectID, &allowed); err != nil || objectID == "" || !allowed {
		t.Fatal("fixture must start with an external file and active product entitlement", err)
	}
	var auditBefore int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_type='download' AND entity_id=$1", fmt.Sprint(fid)).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	block, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	if _, err := block.Exec(t.Context(), "SELECT id FROM file_storage_objects WHERE id=$1 FOR UPDATE", objectID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(h, "GET", fmt.Sprintf("/api/v1/downloads/%d/file", fid), nil, customer, "")
	}()
	waitQueryLock(t, db, `SELECT profile_id,location_sha256,kind,object_key,size_bytes,sha256,state FROM file_storage_objects WHERE id=$1 FOR SHARE`)
	changeCtx, cancelChange := context.WithTimeout(t.Context(), 7*time.Second)
	_, err = db.Pool.Exec(changeCtx, "UPDATE services SET status='suspended' WHERE id=$1", sid)
	cancelChange()
	if err != nil {
		t.Fatal("unrelated service state changes must remain independent of the file catalog lock", err)
	}
	if err := block.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 403)
		if !strings.Contains(out.Body.String(), "download_product_required") || strings.Contains(out.Body.String(), "下载内容 UTF-8") {
			t.Fatal("suspended service retained stale file eligibility or exposed bytes")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("file request did not finish after its object lock was released")
	}
	var downloads, auditAfter int
	if err := db.Pool.QueryRow(t.Context(), "SELECT downloads FROM downloads WHERE id=$1", fid).Scan(&downloads); err != nil || downloads != 0 {
		t.Fatal("denied external download incremented its transfer counter", downloads, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE entity_type='download' AND entity_id=$1", fmt.Sprint(fid)).Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatal("denied external download recorded a successful business operation", auditAfter, auditBefore, err)
	}
}
