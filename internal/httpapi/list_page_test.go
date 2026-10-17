package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestListPageFiltersBindingEmptyPagesAndErrors(t *testing.T) {
	db, _ := testServer(t)
	ctx := t.Context()
	if _, err := db.Pool.Exec(ctx, `CREATE TABLE page_probe(id int,tenant int,label text);
 INSERT INTO page_probe VALUES(1,10,'visible'),(2,20,'hidden'),(3,10,'visible'),(4,10,'visible')`); err != nil {
		t.Fatal(err)
	}
	type item struct {
		ID    int    `db:"id"`
		Label string `db:"label"`
	}
	backing := []any{10, "visible", "do not overwrite", "or this"}
	args := backing[:2]
	countSQL := "SELECT count(*) FROM page_probe WHERE tenant=$1 AND label=$2"
	selectSQL := "SELECT id,label FROM page_probe WHERE tenant=$1 AND label=$2 ORDER BY id DESC"
	page, err := listPage(ctx, db.Pool, countSQL, selectSQL, args, 1, 1, pgx.RowToStructByName[item])
	if err != nil {
		t.Fatal(err)
	}
	items := page["items"].([]item)
	if page["total"] != int64(3) || page["limit"] != 1 || page["offset"] != 1 || len(items) != 1 || items[0].ID != 3 || backing[2] != "do not overwrite" || backing[3] != "or this" {
		t.Fatalf("filter/order/pagination or argument ownership changed: %#v, %#v", page, backing)
	}
	for _, tc := range []struct {
		args   []any
		offset int
		total  int64
	}{{args, 100, 3}, {[]any{10, "' OR true --"}, 0, 0}} {
		page, err = listPage(ctx, db.Pool, countSQL, selectSQL, tc.args, 2, tc.offset, pgx.RowToMap)
		if err != nil || page["total"] != tc.total {
			t.Fatalf("empty page lost total or filter: %#v, %v", page, err)
		}
		raw, err := json.Marshal(page)
		if err != nil || !strings.Contains(string(raw), `"items":[]`) {
			t.Fatalf("empty items must be an array: %s, %v", raw, err)
		}
	}
	if _, err = listPage(ctx, db.Pool, "SELECT 1/0", selectSQL, nil, 1, 0, pgx.RowToMap); err == nil {
		t.Fatal("count failure ignored")
	}
	if _, err = listPage(ctx, db.Pool, "SELECT 1", "SELECT missing FROM page_probe", nil, 1, 0, pgx.RowToMap); err == nil {
		t.Fatal("row query failure ignored")
	}
	if _, err = listPage(ctx, db.Pool, "SELECT 1", "SELECT label FROM page_probe", nil, 1, 0, pgx.RowTo[int]); err == nil {
		t.Fatal("scan failure ignored")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = listPage(canceled, db.Pool, countSQL, selectSQL, args, 1, 0, pgx.RowToMap); err == nil {
		t.Fatal("canceled query succeeded")
	}
}
