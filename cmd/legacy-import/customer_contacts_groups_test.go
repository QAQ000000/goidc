package main

import (
	"testing"

	"github.com/codex/zjmfgo/internal/legacy"
)

func TestCustomerContactGroupImportPreservesRelationshipsAndEditsOnReplay(t *testing.T) {
	db := importTestDB(t)
	snapshot := importFixture()
	snapshot["customer_groups"] = []legacy.Record{{"id": "77", "group_name": "重点客户", "group_colour": "#0f766e", "discount_percent": "15", "susptermexempt": "1", "separateinvoices": "0"}}
	snapshot["users"][0]["groupid"] = "77"
	snapshot["contacts"] = []legacy.Record{{"id": "501", "uid": "10", "username": "财务联系人", "sex": "0", "avatar": "", "companyname": "示例公司", "email": "finance@example.test", "wechat_id": "99", "country": "中国", "province": "北京", "city": "北京", "region": "朝阳", "address1": "测试路", "address2": "", "postcode": "100000", "phonenumber": "13800000000", "permissions": "profile,invoices", "generalemails": "1", "invoiceemails": "1", "productemails": "0", "supportemails": "1", "status": "1", "create_time": "1700000000", "update_time": "1700000001"}}
	im := newImporter(snapshot)
	if err := im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	group := importedID(t, db, "customer_groups", 77)
	contact := importedID(t, db, "contacts", 501)
	var groupName string
	var customerID, memberGroup int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT name FROM customer_groups WHERE id=$1", group).Scan(&groupName); err != nil || groupName != "重点客户" {
		t.Fatalf("group import: %q/%v", groupName, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT customer_id,group_id FROM customer_group_members WHERE group_id=$1", group).Scan(&customerID, &memberGroup); err != nil || customerID != importedID(t, db, "users", 10) || memberGroup != group {
		t.Fatalf("group membership: %d/%d/%v", customerID, memberGroup, err)
	}
	var owner int64
	var status string
	var permissions []string
	if err := db.Pool.QueryRow(t.Context(), "SELECT customer_id,status,permissions FROM customer_contacts WHERE id=$1", contact).Scan(&owner, &status, &permissions); err != nil || status != "active" || len(permissions) != 2 {
		t.Fatalf("contact import: %d/%s/%v", owner, status, err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE customer_groups SET name='现场分组' WHERE id=$1", group); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE customer_contacts SET username='现场联系人' WHERE id=$1", contact); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM legacy_id_map WHERE entity=ANY($1)", []string{"customer_groups", "contacts"}); err != nil {
		t.Fatal(err)
	}
	replay := newImporter(snapshot)
	if err := replay.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if replay.report.Replayed["customer_groups"] != 1 || replay.report.Replayed["contacts"] != 1 {
		t.Fatalf("replay counts: %#v", replay.report.Replayed)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT name FROM customer_groups WHERE id=$1", group).Scan(&groupName); err != nil || groupName != "现场分组" {
		t.Fatal("replay overwrote group edit")
	}
	var username string
	if err := db.Pool.QueryRow(t.Context(), "SELECT username FROM customer_contacts WHERE id=$1", contact).Scan(&username); err != nil || username != "现场联系人" {
		t.Fatal("replay overwrote contact edit")
	}
}

func TestCustomerContactGroupValidationRejectsDuplicateTargetKeys(t *testing.T) {
	snapshot := importFixture()
	snapshot["customer_groups"] = []legacy.Record{
		{"id": "77", "group_name": "重复组", "group_colour": "#0f766e", "discount_percent": "0", "susptermexempt": "0", "separateinvoices": "0"},
		{"id": "78", "group_name": "重复组", "group_colour": "#0f766e", "discount_percent": "0", "susptermexempt": "0", "separateinvoices": "0"},
	}
	if err := newImporter(snapshot).validate(); err == nil {
		t.Fatal("duplicate group name accepted")
	}
	snapshot["customer_groups"] = []legacy.Record{}
	snapshot["contacts"] = []legacy.Record{
		{"id": "501", "uid": "10", "email": "same@example.test", "sex": "0", "status": "1", "create_time": "0", "update_time": "0"},
		{"id": "502", "uid": "10", "email": "same@example.test", "sex": "0", "status": "1", "create_time": "0", "update_time": "0"},
	}
	if err := newImporter(snapshot).validate(); err == nil {
		t.Fatal("duplicate contact email accepted")
	}
}
