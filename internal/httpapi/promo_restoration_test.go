package httpapi

import (
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestPromoRestorationStatesPaginationAndLegacyFilters(t *testing.T) {
	f := newMutationOrderFixture(t)
	ids := map[string]int64{}
	for _, code := range []string{"ACTIVE", "EXPIRED", "DISABLED", "SCHEDULED", "EXHAUSTED", "ONCE"} {
		w := request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "RESTORE-" + code, "discount_type": "percent", "value": 10, "notes": "restoration-search", "one_time": code == "ONCE"}, f.root, "")
		expect(t, w, 201)
		ids[code] = int64(data(t, w)["id"].(float64))
	}
	// Overlap conditions deliberately: visible state must follow the agreed
	// precedence rather than simply reflecting the enabled checkbox.
	for name, sql := range map[string]string{
		"EXPIRED":   "expires_at=now(),active=false,starts_at=now()-interval '1 day',max_uses=1,used_count=1",
		"DISABLED":  "active=false,starts_at=now()+interval '1 day',max_uses=1,used_count=1",
		"SCHEDULED": "starts_at=now()+interval '1 day',max_uses=1,used_count=1",
		"EXHAUSTED": "max_uses=1,used_count=1",
		"ONCE":      "used_count=1",
	} {
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE promo_codes SET "+sql+" WHERE id=$1", ids[name]); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/admin/promo-codes/page?q=restoration-search"
	for status, total := range map[string]int{"all": 6, "active": 1, "expired": 1, "disabled": 1, "scheduled": 1, "exhausted": 2} {
		w := request(f.h, "GET", base+"&status="+status+"&limit=1", nil, f.root, "")
		expect(t, w, 200)
		d := data(t, w)
		if int(d["total"].(float64)) != total || len(d["items"].([]any)) != 1 {
			t.Fatalf("wrong %s page: %s", status, w.Body.String())
		}
		if status != "all" && d["items"].([]any)[0].(map[string]any)["status"] != status {
			t.Fatalf("row state disagrees with filter: %s", w.Body.String())
		}
	}
	first := request(f.h, "GET", base+"&limit=2&offset=0", nil, f.root, "")
	second := request(f.h, "GET", base+"&limit=2&offset=2", nil, f.root, "")
	expect(t, first, 200)
	expect(t, second, 200)
	seen := map[float64]bool{}
	for _, w := range []*httptest.ResponseRecorder{first, second} {
		for _, row := range data(t, w)["items"].([]any) {
			id := row.(map[string]any)["id"].(float64)
			if seen[id] {
				t.Fatal("page repeated row")
			}
			seen[id] = true
		}
	}
	none := request(f.h, "GET", base+"-absent", nil, f.root, "")
	expect(t, none, 200)
	if data(t, none)["total"] != float64(0) || len(data(t, none)["items"].([]any)) != 0 {
		t.Fatal("empty search not empty")
	}
	legacy := request(f.h, "GET", base+"&active=active", nil, f.root, "")
	expect(t, legacy, 200)
	if data(t, legacy)["total"] != float64(4) {
		t.Fatal("legacy active must remain checkbox filter", legacy.Body.String())
	}
	expect(t, request(f.h, "GET", base+"&status=bogus", nil, f.root, ""), 400)
}

func TestPromoRestorationDateMergeClearAndOptimisticConcurrency(t *testing.T) {
	f := newMutationOrderFixture(t)
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	end := start.Add(48 * time.Hour)
	w := request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "RESTORE-DATE", "discount_type": "percent", "value": 10, "starts_at": start.Format(time.RFC3339), "expires_at": end.Format(time.RFC3339)}, f.root, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/promo-codes/%d", id)
	version := func() int64 {
		var v int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM promo_codes WHERE id=$1", id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := version()
	expect(t, request(f.h, "PATCH", path, map[string]any{"expected_version": before, "starts_at": end.Add(time.Hour).Format(time.RFC3339)}, f.root, "date-invalid"), 400)
	if version() != before {
		t.Fatal("rejected merge changed version")
	}
	expect(t, request(f.h, "PATCH", path, map[string]any{"expected_version": before, "notes": "date omitted"}, f.root, "date-omitted"), 200)
	var gotStart, gotEnd *time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT starts_at,expires_at FROM promo_codes WHERE id=$1", id).Scan(&gotStart, &gotEnd); err != nil || gotStart == nil || gotEnd == nil || !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatal("omitted dates changed", err)
	}
	expect(t, request(f.h, "PATCH", path, map[string]any{"expected_version": version(), "starts_at": nil, "expires_at": ""}, f.root, "date-clear"), 200)
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT starts_at,expires_at FROM promo_codes WHERE id=$1", id).Scan(&gotStart, &gotEnd); err != nil || gotStart != nil || gotEnd != nil {
		t.Fatal("clear dates failed", err)
	}
	v := version()
	var wg sync.WaitGroup
	out := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out <- request(f.h, "PATCH", path, map[string]any{"expected_version": v, "notes": fmt.Sprint("editor-", i)}, f.root, fmt.Sprint("editor-key-", i))
		}(i)
	}
	wg.Wait()
	close(out)
	statuses := map[int]int{}
	for result := range out {
		statuses[result.Code]++
	}
	if statuses[200] != 1 || statuses[409] != 1 || version() != v+1 {
		t.Fatal("concurrent editor silently overwritten", statuses)
	}
}

func TestPromoRestorationKeyedReplayAuditAndPermissions(t *testing.T) {
	f := newMutationOrderFixture(t)
	path := "/api/v1/admin/promo-codes"
	body := map[string]any{"code": "RESTORE-REPLAY", "discount_type": "percent", "value": 10}
	var wg sync.WaitGroup
	out := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); out <- request(f.h, "POST", path, body, f.root, "restore-create-key") }()
	}
	wg.Wait()
	close(out)
	var id int64
	for w := range out {
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("concurrent replay failed %d %s", w.Code, w.Body.String())
		}
		current := int64(data(t, w)["id"].(float64))
		if id != 0 && current != id {
			t.Fatal("replay created different resource")
		}
		id = current
	}
	expect(t, request(f.h, "POST", path, map[string]any{"code": "RESTORE-DIFFERENT", "discount_type": "percent", "value": 10}, f.root, "restore-create-key"), 409)
	var created int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='promo_code.created' AND entity_id=$1", fmt.Sprint(id)).Scan(&created); err != nil || created != 1 {
		t.Fatal("creation audit duplicated", created, err)
	}
	itemPath := fmt.Sprintf("%s/%d", path, id)
	var v int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM promo_codes WHERE id=$1", id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	patch := map[string]any{"expected_version": v, "notes": "once audited"}
	patchOut := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			patchOut <- request(f.h, "PATCH", itemPath, patch, f.root, "restore-patch-key")
		}()
	}
	wg.Wait()
	close(patchOut)
	for w := range patchOut {
		expect(t, w, 200)
	}
	var updated int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='promo_code.updated' AND entity_id=$1", fmt.Sprint(id)).Scan(&updated); err != nil || updated != 1 {
		t.Fatal("update replay audited twice", updated, err)
	}
	expect(t, request(f.h, "PATCH", itemPath, map[string]any{"expected_version": v, "notes": "different"}, f.root, "restore-patch-key"), 409)
	contentRole(t, f.db, f.staffID, "orders.manage")
	expect(t, request(f.h, "GET", path+"/page", nil, f.staff, ""), 403)
	contentRole(t, f.db, f.staffID, "promos.view")
	expect(t, request(f.h, "GET", path+"/page", nil, f.staff, ""), 200)
	expect(t, request(f.h, "POST", path, body, f.staff, "reader-create"), 403)
	contentRole(t, f.db, f.staffID, "promos.view", "promos.manage")
	expect(t, request(f.h, "POST", path, map[string]any{"code": "RESTORE-STAFF", "discount_type": "fixed", "value": 100}, f.staff, "staff-create"), 201)
	contentRole(t, f.db, f.staffID, "promos.view")
	expect(t, request(f.h, "PATCH", itemPath, map[string]any{"expected_version": v + 1, "active": false}, f.staff, "staff-revoked"), 403)
	expect(t, request(f.h, "GET", path+"/page", nil, f.customer, ""), 401)
}
