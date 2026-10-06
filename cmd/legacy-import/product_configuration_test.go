package main

import (
	"testing"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
)

func configurationFixture() legacy.Snapshot {
	s := importFixture()
	s["product_config_groups"] = []legacy.Record{{"id": "100", "name": "云主机配置", "description": "旧配置容器", "upstream_id": "0", "global": "0"}}
	s["product_config_links"] = []legacy.Record{{"id": "101", "gid": "100", "pid": "167"}}
	s["product_config_options"] = []legacy.Record{{"id": "200", "gid": "100", "option_name": "os|操作系统", "option_type": "5", "qty_minimum": "0", "qty_maximum": "0", "order": "2", "hidden": "0", "upgrade": "1", "upstream_id": "0", "auto": "0", "is_discount": "1", "notes": "保留旧备注", "is_rebate": "1", "qty_stage": "0", "unit": "", "senior": "1", "linkage_pid": "0", "linkage_top_pid": "0", "linkage_level": "0", "copy_id": "0"}}
	s["product_config_option_subs"] = []legacy.Record{{"id": "300", "config_id": "200", "qty_minimum": "0", "qty_maximum": "0", "option_name": "1|Ubuntu^Ubuntu 24.04", "sort_order": "1", "hidden": "0", "upstream_id": "0", "linkage_pid": "0", "linkage_top_pid": "0", "linkage_level": "0", "copy_id": "0"}}
	s["product_config_prices"] = []legacy.Record{{"id": "400", "relid": "300", "currency": "7", "osetupfee": "0.00", "hsetupfee": "0.00", "dsetupfee": "0.00", "msetupfee": "5.00", "qsetupfee": "14.00", "ssetupfee": "26.00", "asetupfee": "50.00", "bsetupfee": "96.00", "tsetupfee": "138.00", "onetime": "60.00", "hour": "1.00", "day": "2.00", "monthly": "5.00", "quarterly": "14.00", "semiannually": "26.00", "annually": "50.00", "biennially": "96.00", "triennially": "138.00"}}
	s["service_option_selections"] = []legacy.Record{{"id": "500", "relid": "40", "configid": "200", "optionid": "300", "qty": "1"}}
	return s
}

func TestImportedHierarchyKeepsBranchesAndPreservesNativeEditOnReplay(t *testing.T) {
	db := importTestDB(t)
	s := configurationFixture()
	s["product_config_options"][0]["option_type"] = "20"
	s["product_config_options"] = append(s["product_config_options"], legacy.Record{"id": "201", "gid": "100", "option_name": "zone|可用区", "option_type": "20", "linkage_pid": "200", "hidden": "0"})
	s["product_config_option_subs"] = append(s["product_config_option_subs"], legacy.Record{"id": "301", "config_id": "200", "option_name": "other|其他区域", "linkage_pid": "0", "hidden": "0"}, legacy.Record{"id": "302", "config_id": "201", "option_name": "zone-a|区域 A", "linkage_pid": "300", "hidden": "0"})
	if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var pid, root, other, child int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM products WHERE legacy_source='integration' AND legacy_id=167").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for oldID, target := range map[int64]*int64{300: &root, 301: &other, 302: &child} {
		if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM product_options WHERE legacy_source='integration' AND legacy_id=$1", oldID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ValidateProductOptions(t.Context(), db.Pool, pid, []int64{root, child}); err != nil {
		t.Fatal("imported hierarchy lost parent choice", err)
	}
	if _, err := store.ValidateProductOptions(t.Context(), db.Pool, pid, []int64{other, child}); err == nil {
		t.Fatal("wrong imported branch accepted")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_options SET hierarchy_native=true,parent_option_id=$2 WHERE id=$1", child, other); err != nil {
		t.Fatal(err)
	}
	if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateProductOptions(t.Context(), db.Pool, pid, []int64{other, child}); err != nil {
		t.Fatal("replay overwrote native relationship edit", err)
	}
	if _, err := store.ValidateProductOptions(t.Context(), db.Pool, pid, []int64{root, child}); err == nil {
		t.Fatal("replay restored old branch")
	}
}

func TestProductConfigurationImportPreservesMatrixAndServiceSelection(t *testing.T) {
	db := importTestDB(t)
	s := configurationFixture()
	im := newImporter(s)
	if err := im.apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var groups, options, prices, selections int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_groups WHERE legacy_source='integration' AND legacy_id=200").Scan(&groups); err != nil || groups != 1 {
		t.Fatalf("configuration group count=%d err=%v", groups, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_options WHERE legacy_source='integration' AND legacy_id=300").Scan(&options); err != nil || options != 1 {
		t.Fatalf("configuration option count=%d err=%v", options, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_prices WHERE option_id=(SELECT id FROM product_options WHERE legacy_source='integration' AND legacy_id=300)").Scan(&prices); err != nil || prices != 9 {
		t.Fatalf("price matrix count=%d err=%v", prices, err)
	}
	var monthly, annual int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT price_cents FROM product_option_prices WHERE option_id=(SELECT id FROM product_options WHERE legacy_source='integration' AND legacy_id=300) AND billing_cycle='monthly'").Scan(&monthly); err != nil || monthly != 500 {
		t.Fatalf("monthly price=%d err=%v", monthly, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT price_cents FROM product_option_prices WHERE option_id=(SELECT id FROM product_options WHERE legacy_source='integration' AND legacy_id=300) AND billing_cycle='annually'").Scan(&annual); err != nil || annual != 5000 {
		t.Fatalf("annual price=%d err=%v", annual, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM service_option_selections WHERE legacy_source='integration' AND legacy_id=500").Scan(&selections); err != nil || selections != 1 {
		t.Fatalf("service selection count=%d err=%v", selections, err)
	}
	if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{"SELECT count(*) FROM product_option_groups WHERE legacy_source='integration' AND legacy_id=200": 1, "SELECT count(*) FROM product_options WHERE legacy_source='integration' AND legacy_id=300": 1, "SELECT count(*) FROM product_option_prices WHERE option_id=(SELECT id FROM product_options WHERE legacy_source='integration' AND legacy_id=300)": 9, "SELECT count(*) FROM service_option_selections WHERE legacy_source='integration' AND legacy_id=500": 1} {
		var got int
		if err := db.Pool.QueryRow(t.Context(), query).Scan(&got); err != nil || got != want {
			t.Fatalf("replay query=%s got=%d want=%d err=%v", query, got, want, err)
		}
	}
}

func TestProductConfigurationValidationRejectsDuplicatePricing(t *testing.T) {
	s := configurationFixture()
	s["product_config_prices"] = append(s["product_config_prices"], legacy.Record{"id": "401", "relid": "300", "currency": "7"})
	if err := newImporter(s).validate(); err == nil {
		t.Fatal("duplicate configuration prices must be rejected")
	}
}

func TestProductConfigurationValidationRejectsOverlappingQuantityTiers(t *testing.T) {
	s := importFixture()
	s["product_config_groups"] = []legacy.Record{{"id": "100", "name": "数量配置", "global": "0"}}
	s["product_config_links"] = []legacy.Record{{"id": "101", "gid": "100", "pid": "167"}}
	s["product_config_options"] = []legacy.Record{{"id": "200", "gid": "100", "option_name": "disk", "option_type": "4", "qty_minimum": "1", "qty_maximum": "10"}}
	s["product_config_option_subs"] = []legacy.Record{{"id": "301", "config_id": "200", "option_name": "1-5", "qty_minimum": "1", "qty_maximum": "5"}, {"id": "302", "config_id": "200", "option_name": "5-10", "qty_minimum": "5", "qty_maximum": "10"}}
	s["product_config_prices"] = nil
	if err := newImporter(s).validate(); err == nil {
		t.Fatal("overlapping quantity tiers accepted")
	}
}

func TestQuantityConfigurationImportCollapsesLegacyTiers(t *testing.T) {
	db := importTestDB(t)
	s := importFixture()
	s["product_config_groups"] = []legacy.Record{{"id": "100", "name": "云主机配置", "description": "数量配置", "upstream_id": "0", "global": "0"}}
	s["product_config_links"] = []legacy.Record{{"id": "101", "gid": "100", "pid": "167"}}
	s["product_config_options"] = []legacy.Record{{"id": "200", "gid": "100", "option_name": "disk|磁盘", "option_type": "4", "qty_minimum": "1", "qty_maximum": "6", "order": "1", "hidden": "0", "upgrade": "1", "qty_stage": "1", "unit": "GB"}}
	s["product_config_option_subs"] = []legacy.Record{
		{"id": "301", "config_id": "200", "qty_minimum": "1", "qty_maximum": "3", "option_name": "1-3|基础磁盘", "sort_order": "1", "hidden": "0"},
		{"id": "302", "config_id": "200", "qty_minimum": "4", "qty_maximum": "6", "option_name": "4-6|扩展磁盘", "sort_order": "2", "hidden": "0"},
	}
	price := func(id, relid, monthly, setup string) legacy.Record {
		return legacy.Record{"id": id, "relid": relid, "currency": "7", "osetupfee": setup, "hsetupfee": setup, "dsetupfee": setup, "msetupfee": setup, "qsetupfee": setup, "ssetupfee": setup, "asetupfee": setup, "bsetupfee": setup, "tsetupfee": setup, "onetime": monthly, "hour": monthly, "day": monthly, "monthly": monthly, "quarterly": monthly, "semiannually": monthly, "annually": monthly, "biennially": monthly, "triennially": monthly}
	}
	s["product_config_prices"] = []legacy.Record{price("401", "301", "5.00", "1.00"), price("402", "302", "4.00", "2.00")}
	s["service_option_selections"] = []legacy.Record{{"id": "500", "relid": "40", "configid": "200", "optionid": "302", "qty": "5"}}
	if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	var options, aliases, tiers int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_options WHERE legacy_source='integration' AND group_id IN (SELECT id FROM product_option_groups WHERE legacy_source='integration' AND legacy_id=200)").Scan(&options); err != nil || options != 1 {
		t.Fatalf("quantity options=%d err=%v", options, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM legacy_product_config_option_aliases WHERE legacy_source='integration'").Scan(&aliases); err != nil || aliases != 2 {
		t.Fatalf("quantity aliases=%d err=%v", aliases, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_price_tiers WHERE legacy_source='integration'").Scan(&tiers); err != nil || tiers != 18 {
		t.Fatalf("quantity tiers=%d err=%v", tiers, err)
	}
	var first, second int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT price_cents FROM product_option_price_tiers WHERE legacy_source='integration' AND qty_minimum=1 AND qty_maximum=3 AND billing_cycle='monthly'").Scan(&first); err != nil || first != 500 {
		t.Fatalf("first tier=%d err=%v", first, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT price_cents FROM product_option_price_tiers WHERE legacy_source='integration' AND qty_minimum=4 AND qty_maximum=6 AND billing_cycle='monthly'").Scan(&second); err != nil || second != 400 {
		t.Fatalf("second tier=%d err=%v", second, err)
	}
	var optionID, selectionOption int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM product_options WHERE legacy_source='integration' AND legacy_id=301").Scan(&optionID); err != nil {
		t.Fatal(err)
	}
	var optionMinimum, optionMaximum int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT qty_minimum,qty_maximum FROM product_options WHERE id=$1", optionID).Scan(&optionMinimum, &optionMaximum); err != nil || optionMinimum != 1 || optionMaximum != 6 {
		t.Fatalf("canonical quantity bounds=%d-%d err=%v", optionMinimum, optionMaximum, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT option_id FROM service_option_selections WHERE legacy_source='integration' AND legacy_id=500").Scan(&selectionOption); err != nil || selectionOption != optionID {
		t.Fatalf("service option=%d canonical=%d err=%v", selectionOption, optionID, err)
	}
	if err := newImporter(s).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_price_tiers WHERE legacy_source='integration'").Scan(&tiers); err != nil || tiers != 18 {
		t.Fatalf("replayed quantity tiers=%d err=%v", tiers, err)
	}
}
