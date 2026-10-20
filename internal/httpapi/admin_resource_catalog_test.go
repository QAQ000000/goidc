package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestAdminResourcesExactFiltersAndProjection(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "resource-exact-root", "super_admin")
	staffID, staff := user(t, db, h, "resource-exact-staff", "admin")
	owner, customer := user(t, db, h, "resource-exact-owner", "customer")
	other, _ := user(t, db, h, "resource-exact-other", "customer")
	contentRole(t, db, staffID, "resources.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", owner, staffID); err != nil {
		t.Fatal(err)
	}
	first, second := product(t, db, 10), product(t, db, 10)
	sid, _ := billingSchedulerFixture(t, db, owner, first, 0, false)
	billingSchedulerFixture(t, db, owner, second, 1, false)
	billingSchedulerFixture(t, db, other, first, 2, false)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET resource_info='{"ips":["203.0.113.91"],"password":"private-resource-note","token":"private-resource-token"}' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	const endpoint = "/api/v1/admin/resources"
	for _, cookie := range []string{"", customer} {
		expect(t, request(h, "GET", endpoint, nil, cookie, ""), http.StatusUnauthorized)
	}
	for _, field := range []string{"customer_id", "product_id"} {
		for _, value := range []string{"", "0", "-1", "+1", " 1", "1 ", "1x", "1.0", "１", "9223372036854775808"} {
			response := request(h, "GET", endpoint+"?"+field+"="+url.QueryEscape(value), nil, staff, "")
			expect(t, response, http.StatusBadRequest)
			code := "invalid_customer"
			if field == "product_id" {
				code = "invalid_product"
			}
			if errorData(t, response)["code"] != code {
				t.Fatalf("wrong %s validation error: %s", field, response.Body.String())
			}
		}
		expect(t, request(h, "GET", endpoint+"?"+field+"=1&"+field+"=1", nil, staff, ""), http.StatusBadRequest)
	}
	var missing string
	for _, id := range []int64{other, staffID, 9223372036854775807} {
		response := request(h, "GET", fmt.Sprintf("%s?customer_id=%d", endpoint, id), nil, staff, "")
		expect(t, response, http.StatusNotFound)
		failure := errorData(t, response)
		body := fmt.Sprint(failure["code"], failure["message"])
		if failure["code"] != "not_found" || (missing != "" && missing != body) {
			t.Fatal("missing customer and scope denial were distinguishable")
		}
		missing = body
	}
	for _, tc := range []struct {
		query string
		total int
	}{
		{"", 2}, {fmt.Sprintf("?customer_id=%d", owner), 2},
		{fmt.Sprintf("?product_id=%d", first), 1},
		{fmt.Sprintf("?customer_id=%d&product_id=%d&ip=203.0.113.91", owner, first), 1},
		{fmt.Sprintf("?customer_id=%d&product_id=%d&offset=100", owner, first), 1},
		{"?product_id=9223372036854775807", 0},
	} {
		response := request(h, "GET", endpoint+tc.query, nil, staff, "")
		expect(t, response, http.StatusOK)
		page := data(t, response)
		if page["total"] != float64(tc.total) || strings.Contains(response.Body.String(), "private-resource-") {
			t.Fatal("exact resource query count or privacy mismatch", response.Body.String())
		}
		for _, entry := range page["items"].([]any) {
			row := entry.(map[string]any)
			if row["product_name"] != "商品" || row["product_id"] == nil || row["next_due_at"] == nil || row["user_id"] != float64(owner) {
				t.Fatal("resource product, due date or owner projection missing", row)
			}
		}
		if tc.query != "" && strings.Contains(tc.query, "offset=100") && len(page["items"].([]any)) != 0 {
			t.Fatal("resource pagination ignored offset")
		}
	}
	// The root can read the same product across owners. Staff scope never expands.
	response := request(h, "GET", fmt.Sprintf("%s?product_id=%d", endpoint, first), nil, root, "")
	if data(t, response)["total"] != float64(2) {
		t.Fatal("global product filtering did not preserve customer scope")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", owner); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("%s?customer_id=%d", endpoint, owner), nil, staff, ""), http.StatusNotFound)
	contentRole(t, db, staffID, "products.view")
	expect(t, request(h, "GET", endpoint, nil, staff, ""), http.StatusForbidden)
}

func TestAdminCatalogGroupsPageFiltersAndCompatibility(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "catalog-group-page-root", "super_admin")
	readerID, reader := user(t, db, h, "catalog-group-page-reader", "admin")
	_, customer := user(t, db, h, "catalog-group-page-customer", "customer")
	contentRole(t, db, readerID, "products.view")
	sid := catalogGroupID(t, h, root, "section", catalogGroupInput("first-section", nil))
	inactive := catalogGroupInput("inactive-section", nil)
	inactive.Active = new(bool)
	other := catalogGroupID(t, h, root, "section", inactive)
	cids := []int64{}
	for i := range 3 {
		in := catalogGroupInput(fmt.Sprintf("page-category-%d", i), &sid)
		in.SortOrder = 3 - i
		if i == 2 {
			in.Active = new(bool)
		}
		cids = append(cids, catalogGroupID(t, h, root, "category", in))
	}
	otherCategory := catalogGroupID(t, h, root, "category", catalogGroupInput("other-category", &other))
	managedProductID(t, h, root, productInput("member-product", &cids[1]))
	const endpoint = "/api/v1/admin/catalog-groups/category/page"
	for _, cookie := range []string{"", customer} {
		expect(t, request(h, "GET", endpoint, nil, cookie, ""), http.StatusUnauthorized)
	}
	response := request(h, "GET", fmt.Sprintf("%s?section_id=%d&q=page-category&limit=1&offset=1", endpoint, sid), nil, reader, "")
	expect(t, response, http.StatusOK)
	page := data(t, response)
	items := page["items"].([]any)
	if page["total"] != float64(3) || page["limit"] != float64(1) || page["offset"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["id"] != float64(cids[1]) || items[0].(map[string]any)["member_count"] != float64(1) {
		t.Fatal("category sorting, count or pagination mismatch", page)
	}
	for _, tc := range []struct {
		query string
		count int
	}{
		{"?active=true", 3}, {"?active=false", 1}, {"?q=first-section", 0},
		{fmt.Sprintf("?section_id=%d&active=true", sid), 2}, {"?q=page-category&offset=100", 3},
		{"?section_id=9223372036854775807", 0},
	} {
		response = request(h, "GET", endpoint+tc.query, nil, reader, "")
		expect(t, response, http.StatusOK)
		if data(t, response)["total"] != float64(tc.count) {
			t.Fatal("category filter mismatch", tc.query, response.Body.String())
		}
		if strings.Contains(tc.query, "offset=100") && len(data(t, response)["items"].([]any)) != 0 {
			t.Fatal("category offset was ignored")
		}
	}
	response = request(h, "GET", fmt.Sprintf("%s?section_id=%d", endpoint, other), nil, reader, "")
	row := data(t, response)["items"].([]any)[0].(map[string]any)
	if row["id"] != float64(otherCategory) || row["section_active"] != false {
		t.Fatal("inactive parent metadata lost", row)
	}
	for _, query := range []string{"section_id=", "section_id=0", "section_id=%2B1", "section_id=1&section_id=1", "active=1", "active=true&active=false", "q=a&q=b", "q=" + strings.Repeat("x", 121)} {
		invalid := request(h, "GET", endpoint+"?"+query, nil, reader, "")
		expect(t, invalid, http.StatusBadRequest)
		code := "invalid_catalog_filter"
		if strings.HasPrefix(query, "section_id=") {
			code = "invalid_section"
		}
		if !strings.Contains(invalid.Body.String(), `"code":"`+code+`"`) {
			t.Fatal("unexpected catalog filter error", query, invalid.Body.String())
		}
	}
	expect(t, request(h, "GET", "/api/v1/admin/catalog-groups/section/page?section_id=1", nil, reader, ""), http.StatusBadRequest)
	expect(t, request(h, "GET", "/api/v1/admin/catalog-groups/unknown/page", nil, reader, ""), http.StatusBadRequest)
	response = request(h, "GET", "/api/v1/admin/catalog-groups/section/page?active=false", nil, reader, "")
	if data(t, response)["total"] != float64(1) {
		t.Fatal("section page did not filter inactive groups")
	}
	if len(listData(t, request(h, "GET", "/api/v1/admin/catalog-groups/category", nil, reader, ""))) != 4 || len(listData(t, request(h, "GET", "/api/v1/admin/catalog-groups/section", nil, reader, ""))) != 2 {
		t.Fatal("legacy catalog group arrays were truncated or filtered")
	}
	contentRole(t, db, readerID, "customers.view")
	expect(t, request(h, "GET", endpoint, nil, reader, ""), http.StatusForbidden)
}

func TestProductConfigurationNotesAdminOnlyAndPreservation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "config-notes-root", "super_admin")
	readerID, reader := user(t, db, h, "config-notes-reader", "admin")
	contentRole(t, db, readerID, "products.view")
	pid := managedProductID(t, h, root, productInput("config-notes-product", nil))
	path := fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid)
	notes := "private-config-note\n" + strings.Repeat("备", 4980)
	in := store.ProductOptionGroupInput{Name: "Notes", Slug: "notes", OptionType: 1, Active: enabled(), Notes: &notes, ExpectedRevision: configRevision(t, h, root, pid)}
	response := request(h, "POST", path, in, root, "")
	expect(t, response, http.StatusCreated)
	gid := int64(data(t, response)["id"].(float64))
	path += fmt.Sprintf("/%d", gid)
	readNotes := func() string {
		t.Helper()
		response := request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, reader, "")
		expect(t, response, http.StatusOK)
		return data(t, response)["option_groups"].([]any)[0].(map[string]any)["notes"].(string)
	}
	if readNotes() != notes {
		t.Fatal("admin configuration notes did not roundtrip")
	}
	for _, public := range []string{fmt.Sprintf("/api/v1/catalog/%d", pid), "/api/v1/catalog", "/api/v1/catalog/page"} {
		response := request(h, "GET", public, nil, "", "")
		expect(t, response, http.StatusOK)
		if strings.Contains(response.Body.String(), "private-config-note") || strings.Contains(response.Body.String(), `"notes":`) {
			t.Fatal("private configuration notes entered public projection", public)
		}
	}
	in.Options, in.Notes = nil, nil
	in.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", path, in, reader, ""), http.StatusForbidden)
	expect(t, request(h, "PUT", path, in, root, ""), http.StatusOK)
	if readNotes() != notes {
		t.Fatal("old client omission erased notes")
	}
	// A stale writer cannot clear current notes.
	empty := ""
	in.Notes = &empty
	expect(t, request(h, "PUT", path, in, root, ""), http.StatusConflict)
	in.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", path, in, root, ""), http.StatusOK)
	if readNotes() != "" {
		t.Fatal("explicit empty notes were not cleared")
	}
	for _, invalid := range []string{strings.Repeat("备", 5001), "bad\x00notes"} {
		in.Notes, in.ExpectedRevision = &invalid, configRevision(t, h, root, pid)
		expect(t, request(h, "PUT", path, in, root, ""), http.StatusConflict)
		if readNotes() != "" {
			t.Fatal("invalid notes changed the persisted group")
		}
	}
	maxNotes := strings.Repeat("备", 5000)
	in.Notes, in.ExpectedRevision = &maxNotes, configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", path, in, root, ""), http.StatusOK)
	if readNotes() != maxNotes {
		t.Fatal("valid 5000-character unicode note was rejected or truncated")
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_config_note_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='product.option_group.updated' THEN RAISE EXCEPTION 'injected notes audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_config_note_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_config_note_audit()`); err != nil {
		t.Fatal(err)
	}
	in.Notes, in.ExpectedRevision = &empty, configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", path, in, root, ""), http.StatusInternalServerError)
	if readNotes() != maxNotes || *configRevision(t, h, root, pid) != *in.ExpectedRevision {
		t.Fatal("audit failure committed configuration notes or revision")
	}
}

func TestGlobalConfigurationNotesProjectionCopyAndDetach(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "global-notes-root", "super_admin")
	first := managedProductID(t, h, root, productInput("global-notes-first", nil))
	second := managedProductID(t, h, root, productInput("global-notes-second", nil))
	notes := "private-shared-config-notes"
	in := store.GlobalConfigurationInput{Name: "Notes group", Definitions: []store.GlobalDefinition{{ProductOptionGroupInput: store.ProductOptionGroupInput{Name: "Backup", Slug: "backup", Active: enabled(), OptionType: 1, Notes: &notes}}}}
	const endpoint = "/api/v1/admin/global-configuration-groups"
	response := request(h, "POST", endpoint, in, root, "global-notes-create")
	expect(t, response, http.StatusOK)
	id := int64(data(t, response)["id"].(float64))
	path := fmt.Sprintf("%s/%d", endpoint, id)
	assign := store.GlobalConfigurationInput{ExpectedVersion: 1, ProductIDs: []int64{first}, ExpectedRevisions: map[int64]int64{first: *configRevision(t, h, root, first)}}
	expect(t, request(h, "PUT", path+"/assignments", assign, root, "global-notes-first-assignment"), http.StatusOK)
	check := func(pid int64, expected string) {
		t.Helper()
		response := request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, root, "")
		expect(t, response, http.StatusOK)
		groups := data(t, response)["option_groups"].([]any)
		if len(groups) != 1 || groups[0].(map[string]any)["notes"] != expected {
			t.Fatal("global notes projection lost", response.Body.String())
		}
		response = request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", "")
		if strings.Contains(response.Body.String(), `"notes":`) {
			t.Fatal("shared notes leaked publicly")
		}
	}
	check(first, notes)
	in.ExpectedVersion = 2
	in.Definitions[0].Notes = nil
	expect(t, request(h, "PUT", path, in, root, "global-notes-omitted"), http.StatusOK)
	check(first, notes)
	copyResponse := request(h, "POST", endpoint+"/copy", store.GlobalConfigurationInput{Name: "Notes copy", SourceID: id, ExpectedVersion: 3}, root, "global-notes-copy")
	expect(t, copyResponse, http.StatusOK)
	copyID := int64(data(t, copyResponse)["id"].(float64))
	copyDetail := data(t, request(h, "GET", fmt.Sprintf("%s/%d", endpoint, copyID), nil, root, ""))
	if copyDetail["definitions"].([]any)[0].(map[string]any)["notes"] != notes {
		t.Fatal("copy lost omitted-and-preserved notes")
	}
	assign.ExpectedVersion = 3
	assign.ProductIDs = []int64{first, second}
	assign.ExpectedRevisions = map[int64]int64{first: *configRevision(t, h, root, first), second: *configRevision(t, h, root, second)}
	expect(t, request(h, "PUT", path+"/assignments", assign, root, "global-notes-second-assignment"), http.StatusOK)
	check(second, notes)
	assign.ExpectedVersion = 4
	assign.ProductIDs = []int64{second}
	assign.ExpectedRevisions = map[int64]int64{first: *configRevision(t, h, root, first), second: *configRevision(t, h, root, second)}
	assign.AcknowledgeDetach = true
	expect(t, request(h, "PUT", path+"/assignments", assign, root, "global-notes-detach"), http.StatusOK)
	empty := ""
	in.ExpectedVersion = 5
	in.Definitions[0].Notes = &empty
	expect(t, request(h, "PUT", path, in, root, "global-notes-clear"), http.StatusOK)
	check(first, notes)
	check(second, "")
	tooLong := strings.Repeat("备", 5001)
	in.ExpectedVersion = 6
	in.Definitions[0].Notes = &tooLong
	expect(t, request(h, "PUT", path, in, root, "global-notes-invalid"), http.StatusConflict)
	check(first, notes)
	check(second, "")
}
