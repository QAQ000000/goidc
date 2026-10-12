package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/store"
)

func TestHierarchyMigrationPreservesLegacyReferencesAndEnablesNativeDefaults(t *testing.T) {
	db := emptyImportTestDB(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name >= "053_" {
			break
		}
		raw, readErr := migrations.Files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = db.Pool.Exec(t.Context(), string(raw)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if _, err = db.Pool.Exec(t.Context(), "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
	}
	var pid, root, child, rootOption, childOption int64
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO products(name,slug,product_type,billing_cycle,price_cents) VALUES('hierarchy upgrade','hierarchy-upgrade','server','monthly',2000) RETURNING id").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO product_option_groups(product_id,name,slug,required,option_type,legacy_source,legacy_id,legacy_metadata) VALUES($1,'root','root',true,20,'upgrade',100,'{"linkage_pid":"0"}') RETURNING id`, pid).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO product_option_groups(product_id,name,slug,required,option_type,legacy_source,legacy_id,legacy_metadata) VALUES($1,'child','child',true,20,'upgrade',200,'{"linkage_pid":"100"}') RETURNING id`, pid).Scan(&child); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO product_options(group_id,label,value,legacy_source,legacy_id,legacy_metadata) VALUES($1,'parent','parent','upgrade',300,'{"linkage_pid":"0"}') RETURNING id`, root).Scan(&rootOption); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO product_options(group_id,label,value,legacy_source,legacy_id,legacy_metadata) VALUES($1,'choice','choice','upgrade',400,'{"linkage_pid":"300"}') RETURNING id`, child).Scan(&childOption); err != nil {
		t.Fatal(err)
	}
	var before, after string
	query := `SELECT jsonb_build_object('groups',(SELECT jsonb_agg(to_jsonb(g)%s ORDER BY id) FROM product_option_groups g),'options',(SELECT jsonb_agg(to_jsonb(o)%s ORDER BY id) FROM product_options o))::text`
	if err = db.Pool.QueryRow(t.Context(), fmt.Sprintf(query, "", "")).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), fmt.Sprintf(query, "-'parent_group_id'-'hierarchy_native'-'global_group_id'", "-'parent_option_id'-'hierarchy_native'")).Scan(&after); err != nil || before != after {
		t.Fatal("upgrade altered legacy records", err)
	}
	var unmanaged bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT bool_and(global_group_id IS NULL) FROM product_option_groups WHERE product_id=$1", pid).Scan(&unmanaged); err != nil || !unmanaged {
		t.Fatal("upgrade assigned legacy groups to global ownership", err)
	}
	if _, err = store.ValidateProductOptions(t.Context(), db.Pool, pid, []int64{rootOption, childOption}); err != nil {
		t.Fatal("legacy branch lost after upgrade", err)
	}
	if _, err = store.ValidateProductOptions(t.Context(), db.Pool, pid, []int64{childOption}); err == nil {
		t.Fatal("legacy dependency disappeared")
	}
	var oldNative bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT hierarchy_native FROM product_option_groups WHERE id=$1", root).Scan(&oldNative); err != nil || oldNative {
		t.Fatal("old record lost compatibility mode", err)
	}
	var newNative bool
	if err = db.Pool.QueryRow(t.Context(), "INSERT INTO product_option_groups(product_id,name,slug) VALUES($1,'native','native') RETURNING hierarchy_native", pid).Scan(&newNative); err != nil || !newNative {
		t.Fatal("native defaults missing", err)
	}
}
