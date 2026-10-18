package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Older reviewed snapshots can omit categories. A new snapshot with a positive
// gid must contain its category, never substitute a navigation group.
func (im *importer) validateProductCategories() error {
	categories := index(im.source["product_categories"])
	for _, r := range im.source["product_categories"] {
		if strings.TrimSpace(r["name"]) == "" || len([]rune(r["name"])) > 255 || referenceID(r, "order") < 0 || referenceID(r, "order") > 1000000 || r["hidden"] != "0" && r["hidden"] != "1" {
			return fmt.Errorf("invalid product category: %d", sourceID(r))
		}
	}
	for _, r := range im.source["products"] {
		if _, exists := r["gid"]; !exists {
			continue
		}
		id, err := navigationReference(r, "gid")
		if err != nil {
			return err
		}
		if id > 0 && categories[id] == nil {
			return fmt.Errorf("missing product category: products/%d/category/%d", sourceID(r), id)
		}
	}
	return nil
}

func (im *importer) importProductCategories(ctx context.Context, tx pgx.Tx, runID int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	for _, r := range im.source["product_categories"] {
		if replayed, err := im.replay(ctx, tx, "product_categories", r, out); err != nil {
			return nil, err
		} else if replayed {
			continue
		}
		if err := im.navigationRecordIsNew(ctx, tx, "product_categories", r); err != nil {
			return nil, err
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO product_categories(name,slug,sort_order,active,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, strings.TrimSpace(r["name"]), slug(sourceID(r)), referenceID(r, "order"), r["hidden"] != "1", im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return nil, err
		}
		out[sourceID(r)] = id
		im.report.Mapped["product_categories"]++
		if err := im.recordMapped(ctx, tx, "product_categories", r, id, runID); err != nil {
			return nil, err
		}
	}
	return out, nil
}
