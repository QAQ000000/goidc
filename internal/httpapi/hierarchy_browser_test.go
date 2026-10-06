package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestHierarchyBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "hierarchy-super", func(db *store.Store, h http.Handler) {
		_, admin := user(t, db, h, "hierarchy-fixture-admin", "super_admin")
		user(t, db, h, "hierarchy-customer", "customer")
		input := productInput("hierarchy-preview", nil)
		input.Name = "多区域云服务器"
		pid := managedProductID(t, h, admin, input)
		makeGroup := func(name string, values ...string) int64 {
			options := []store.ProductOptionInput{}
			for i, value := range values {
				options = append(options, store.ProductOptionInput{Label: value, Value: value, Active: enabled(), SortOrder: i, PriceDeltaCents: int64((i + 1) * 100)})
			}
			return configGroup(t, h, admin, pid, name, true, options)
		}
		region := makeGroup("region", "广州", "杭州", "独立区域")
		zone := makeGroup("zone", "华南一区", "华南二区", "华东一区")
		plan := makeGroup("plan", "华南基础型", "华南计算型", "华东基础型")
		gz := configOption(t, h, admin, pid, region, "广州")
		hz := configOption(t, h, admin, pid, region, "杭州")
		gz1 := configOption(t, h, admin, pid, zone, "华南一区")
		gz2 := configOption(t, h, admin, pid, zone, "华南二区")
		hz1 := configOption(t, h, admin, pid, zone, "华东一区")
		gzSmall := configOption(t, h, admin, pid, plan, "华南基础型")
		gzCompute := configOption(t, h, admin, pid, plan, "华南计算型")
		hzSmall := configOption(t, h, admin, pid, plan, "华东基础型")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE product_option_groups SET hierarchy_native=false,option_type=20,legacy_source='hierarchy-browser',legacy_id=id,legacy_metadata=jsonb_build_object('linkage_pid',CASE id WHEN $1 THEN $2 WHEN $3 THEN $1 ELSE 0 END),name=CASE id WHEN $2 THEN '区域' WHEN $1 THEN '可用区' ELSE '规格' END,sort_order=CASE id WHEN $2 THEN 30 WHEN $1 THEN 20 ELSE 10 END WHERE product_id=$4`, zone, region, plan, pid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE product_options SET hierarchy_native=false,legacy_source='hierarchy-browser',legacy_id=id,legacy_metadata=jsonb_build_object('linkage_pid',CASE id WHEN $1 THEN $2 WHEN $3 THEN $2 WHEN $4 THEN $5 WHEN $6 THEN $1 WHEN $7 THEN $3 WHEN $8 THEN $4 ELSE 0 END) WHERE group_id=ANY($9)`, gz1, gz, gz2, hz1, hz, gzSmall, gzCompute, hzSmall, []int64{region, zone, plan}); err != nil {
			t.Fatal(err)
		}
	})
}
