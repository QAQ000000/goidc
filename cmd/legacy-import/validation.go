package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/jackc/pgx/v5"
)

func (im *importer) validate() error {
	if strings.TrimSpace(im.sourceName) == "" || len(im.sourceName) > 120 {
		return fmt.Errorf("invalid source identity")
	}
	if err := im.validateServiceNavigation(); err != nil {
		return err
	}
	if err := im.validateDownloadFiles(); err != nil {
		return err
	}
	if err := im.validateTickets(); err != nil {
		return err
	}
	if err := im.validateEmailTemplates(); err != nil {
		return err
	}
	if err := im.validateCustomerContactsGroups(); err != nil {
		return err
	}
	if len(im.source["product_config_groups"])+len(im.source["product_config_links"])+len(im.source["product_config_options"])+len(im.source["product_config_option_subs"])+len(im.source["product_config_option_links"])+len(im.source["product_config_prices"])+len(im.source["service_option_selections"]) > 0 {
		if err := im.validateProductConfiguration(); err != nil {
			return err
		}
	}
	fields := map[string][]string{"users": {"credit"}, "orders": {"amount"}, "invoices": {"total", "subtotal", "credit", "tax", "tax2"}, "invoice_items": {"amount"}, "services": {"amount", "firstpaymentamount"}, "prices": {"monthly", "quarterly", "annually", "onetime", "msetupfee", "qsetupfee", "asetupfee", "osetupfee"}}
	for entity, records := range im.source {
		ids := map[int64]bool{}
		for _, r := range records {
			id := sourceID(r)
			if id <= 0 || ids[id] {
				return fmt.Errorf("invalid or duplicate identity: %s/%d", entity, id)
			}
			ids[id] = true
			for _, field := range fields[entity] {
				amount, err := legacy.Cents(r[field])
				priceSentinel := entity == "prices" && !strings.Contains(field, "setupfee") && amount == -100
				if err != nil || (amount < 0 && entity != "invoice_items" && !priceSentinel) {
					return fmt.Errorf("invalid monetary field: %s/%d/%s", entity, id, field)
				}
			}
			if entity == "currencies" && r["code"] != "CNY" {
				return fmt.Errorf("unsupported currency: %s; multi-currency migration is not implemented", r["code"])
			}
			if entity == "users" && r["status"] != "0" && r["status"] != "1" && r["status"] != "2" {
				return fmt.Errorf("unsupported customer state: users/%d", id)
			}
			states := map[string][]string{"orders": {"Pending", "Active", "Completed", "Cancelled", "Fraud", "Refunded"}, "invoices": {"Unpaid", "Paid", "Cancelled", "Refunded", "Overdue"}, "services": {"Pending", "Active", "Suspended", "Cancelled", "Fraud", "Completed", "Deleted"}}
			if allowed, ok := states[entity]; ok {
				raw := r["status"]
				if entity == "services" {
					raw = r["domainstatus"]
				}
				known := false
				for _, state := range allowed {
					known = known || strings.EqualFold(state, raw)
				}
				if !known {
					return fmt.Errorf("unsupported state: %s/%d", entity, id)
				}
			}
		}
	}
	for _, r := range im.source["prices"] {
		for _, field := range []string{"semiannually", "biennially", "triennially", "ssetupfee", "bsetupfee", "tsetupfee"} {
			if raw, ok := r[field]; ok {
				amount, err := legacy.Cents(raw)
				if err != nil || (amount < 0 && (strings.Contains(field, "setupfee") || amount != -100)) {
					return fmt.Errorf("invalid monetary field: prices/%d/%s", sourceID(r), field)
				}
			}
		}
	}
	for _, r := range im.source["services"] {
		if cycle(r["billingcycle"]) == "" {
			return fmt.Errorf("unsupported billing cycle: services/%d", sourceID(r))
		}
	}
	if im.currencyID() == "" {
		return fmt.Errorf("CNY currency definition is required")
	}
	for _, r := range im.source["users"] {
		if r["currency"] != im.currencyID() {
			return fmt.Errorf("unsupported customer currency: users/%d", sourceID(r))
		}
	}
	prices := map[string]bool{}
	for _, r := range im.source["prices"] {
		key := r["relid"] + ":" + r["currency"]
		if priceProductID(r) <= 0 || prices[key] || r["currency"] != im.currencyID() {
			return fmt.Errorf("invalid or duplicate product pricing: prices/%d", sourceID(r))
		}
		prices[key] = true
	}
	invoices := index(im.source["invoices"])
	owners, sums := map[int64]string{}, map[int64]int64{}
	for _, r := range im.source["invoice_items"] {
		if deleted(r) {
			continue
		}
		id := sourceID(legacy.Record{"id": r["invoice_id"]})
		if id <= 0 {
			return fmt.Errorf("invalid invoice reference: invoice_items/%d", sourceID(r))
		}
		if inv := invoices[id]; inv != nil && inv["uid"] != r["uid"] {
			return fmt.Errorf("invoice item owner mismatch: invoice_items/%d", sourceID(r))
		}
		if owner, ok := owners[id]; ok && owner != r["uid"] {
			return fmt.Errorf("invoice item owner mismatch: invoice/%d", id)
		}
		owners[id] = r["uid"]
		amount := cents(r["amount"])
		if (amount > 0 && sums[id] > math.MaxInt64-amount) || (amount < 0 && sums[id] < math.MinInt64-amount) {
			return fmt.Errorf("invoice item sum overflow: invoice/%d", id)
		}
		sums[id] += amount
	}
	for id, amount := range sums {
		if invoices[id] == nil && amount < 0 {
			return fmt.Errorf("negative orphan invoice total: invoice/%d", id)
		}
	}
	return nil
}

func (im *importer) validateProductConfiguration() error {
	groups, links, options, subs := index(im.source["product_config_groups"]), index(im.source["product_config_links"]), index(im.source["product_config_options"]), index(im.source["product_config_option_subs"])
	products, services := index(im.source["products"]), index(im.source["services"])
	seenLinks := map[string]bool{}
	for _, r := range im.source["product_config_groups"] {
		id := sourceID(r)
		if id < 1 || strings.TrimSpace(r["name"]) == "" || len([]rune(r["name"])) > 120 || len([]rune(r["description"])) > 1000 {
			return fmt.Errorf("invalid product config group: %d", id)
		}
	}
	for _, r := range im.source["product_config_links"] {
		id, gid, pid := sourceID(r), referenceID(r, "gid"), referenceID(r, "pid")
		if links[id] == nil || groups[gid] == nil || products[pid] == nil {
			return fmt.Errorf("invalid product config link: %d", id)
		}
		key := fmt.Sprintf("%d:%d", gid, pid)
		if seenLinks[key] {
			return fmt.Errorf("duplicate product config link: %s", key)
		}
		seenLinks[key] = true
	}
	seenOptions := map[int64]bool{}
	for _, r := range im.source["product_config_options"] {
		id, gid := sourceID(r), referenceID(r, "gid")
		if id < 1 || seenOptions[id] || groups[gid] == nil || strings.TrimSpace(r["option_name"]) == "" || len([]rune(r["option_name"])) > 500 {
			return fmt.Errorf("invalid product config option: %d", id)
		}
		seenOptions[id] = true
		min, max := referenceID(r, "qty_minimum"), referenceID(r, "qty_maximum")
		if min < 0 || max < 0 || (max > 0 && max < min) || referenceID(r, "order") < 0 || referenceID(r, "qty_stage") < 0 {
			return fmt.Errorf("invalid product config option range: %d", id)
		}
	}
	seenSubs := map[int64]bool{}
	quantityOptions := map[int64]bool{}
	quantityRanges := map[int64][][2]int64{}
	for _, r := range im.source["product_config_option_subs"] {
		id, option := sourceID(r), referenceID(r, "config_id")
		if id < 1 || seenSubs[id] || options[option] == nil || strings.TrimSpace(r["option_name"]) == "" || len([]rune(r["option_name"])) > 500 {
			return fmt.Errorf("invalid product config sub option: %d", id)
		}
		seenSubs[id] = true
		min, max := referenceID(r, "qty_minimum"), referenceID(r, "qty_maximum")
		if min < 0 || max < 0 || (max > 0 && max < min) || referenceID(r, "sort_order") < 0 {
			return fmt.Errorf("invalid product config sub range: %d", id)
		}
		if legacyQuantityType(intOrZero(options[option]["option_type"])) {
			quantityOptions[option] = true
			low, high := quantityRange(r["qty_minimum"], r["qty_maximum"])
			quantityRanges[option] = append(quantityRanges[option], [2]int64{low, high})
		}
	}
	for option := range quantityOptions {
		ranges := quantityRanges[option]
		sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
		for i := 1; i < len(ranges); i++ {
			if ranges[i][0] <= ranges[i-1][1] {
				return fmt.Errorf("overlapping quantity tiers: %d", option)
			}
		}
	}
	for _, r := range im.source["product_config_option_links"] {
		id, option := sourceID(r), referenceID(r, "config_id")
		if id < 1 || options[option] == nil || len(r["sub_id"]) > 1024 || len(r["relation"]) > 25 || len(r["type"]) > 25 {
			return fmt.Errorf("invalid product config option link: %d", id)
		}
		if relation := referenceID(r, "relation_id"); relation < 0 {
			return fmt.Errorf("invalid product config relation: %d", id)
		}
	}
	seenPrices := map[string]bool{}
	for _, r := range im.source["product_config_prices"] {
		id, option := sourceID(r), referenceID(r, "relid")
		if id < 1 || subs[option] == nil || r["currency"] != im.currencyID() {
			return fmt.Errorf("invalid product config price: %d", id)
		}
		for _, field := range []string{"onetime", "hour", "day", "monthly", "quarterly", "semiannually", "annually", "biennially", "triennially"} {
			amount, err := legacy.Cents(r[field])
			if err != nil || amount < -100 {
				return fmt.Errorf("invalid product config price: %d/%s", id, field)
			}
		}
		for _, field := range []string{"osetupfee", "hsetupfee", "dsetupfee", "msetupfee", "qsetupfee", "ssetupfee", "asetupfee", "bsetupfee", "tsetupfee"} {
			amount, err := legacy.Cents(r[field])
			if err != nil || amount < 0 {
				return fmt.Errorf("invalid product config setup price: %d/%s", id, field)
			}
		}
		if seenPrices[strconv.FormatInt(option, 10)] {
			return fmt.Errorf("duplicate product config price: %d", option)
		}
		seenPrices[strconv.FormatInt(option, 10)] = true
	}
	for _, r := range im.source["service_option_selections"] {
		id, service, option, sub := sourceID(r), referenceID(r, "relid"), referenceID(r, "configid"), referenceID(r, "optionid")
		if id < 1 || services[service] == nil || options[option] == nil || (sub != 0 && subs[sub] == nil) {
			return fmt.Errorf("invalid service config selection: %d", id)
		}
	}
	return nil
}

var targetTables = map[string]string{
	"users": "users", "products": "products", "orders": "orders", "invoices": "invoices",
	"services": "services", "invoice_items": "invoice_items", "order_items": "order_items", "orphan_invoices": "invoices",
	"service_navigation_groups": "service_navigation_groups", "service_navigation_preferences": "service_navigation_preferences",
	"download_categories": "download_categories", "downloads": "downloads", "download_products": "download_products", "customer_files": "customer_files", "email_templates": "notification_templates",
	"customer_groups": "customer_groups", "contacts": "customer_contacts",
	"product_config_groups": "legacy_product_config_groups", "product_config_links": "legacy_product_config_links",
	"product_config_options": "product_option_groups", "product_config_option_subs": "product_options",
	"product_config_option_links": "legacy_product_config_option_links",
	"service_option_selections":   "service_option_selections",
	"staff":                       "users", "ticket_departments": "ticket_departments", "tickets": "tickets", "ticket_replies": "ticket_replies", "ticket_notes": "ticket_notes", "ticket_attachments": "ticket_attachments",
}

func (im *importer) replay(ctx context.Context, tx pgx.Tx, entity string, r legacy.Record, out map[int64]int64) (bool, error) {
	table, ok := targetTables[entity]
	if !ok {
		return false, fmt.Errorf("unsupported mapping entity: %s", entity)
	}
	var target int64
	targetSource := im.targetSource(entity)
	err := tx.QueryRow(ctx, "SELECT target_id FROM legacy_id_map WHERE source=$1 AND entity=$2 AND legacy_id=$3", im.sourceName, entity, sourceID(r)).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		// Older rehearsals have legacy identities and archives but no ID map.
		err = tx.QueryRow(ctx, "SELECT id FROM "+table+" WHERE legacy_source=$1 AND legacy_id=$2", targetSource, sourceID(r)).Scan(&target)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var archived bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1 AND entity=$2 AND legacy_id=$3)", im.sourceName, entity, sourceID(r)).Scan(&archived); err != nil {
			return false, err
		}
		if !archived {
			return false, fmt.Errorf("unverified existing target: %s/%d", entity, sourceID(r))
		}
		if _, err = tx.Exec(ctx, "INSERT INTO legacy_id_map(source,entity,legacy_id,target_id) VALUES($1,$2,$3,$4)", im.sourceName, entity, sourceID(r), target); err != nil {
			return false, err
		}
	}
	if err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1 AND legacy_source=$2 AND legacy_id=$3)", target, targetSource, sourceID(r)).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("stale target mapping: %s/%d", entity, sourceID(r))
	}
	out[sourceID(r)] = target
	im.report.Replayed[entity]++
	return true, nil
}

func (im *importer) verifySnapshot(ctx context.Context, tx pgx.Tx) error {
	var entities []string
	err := tx.QueryRow(ctx, "SELECT entities FROM legacy_snapshot_manifests WHERE source=$1", im.sourceName).Scan(&entities)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var hasArchives bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1)", im.sourceName).Scan(&hasArchives); err != nil {
		return err
	}
	if (hasArchives && entities == nil) || (entities != nil && !slices.Equal(entities, im.snapshotEntities())) {
		return fmt.Errorf("source snapshot contract changed or missing; fresh isolated rehearsal required")
	}
	rows, err := tx.Query(ctx, `SELECT entity,legacy_id,checksum FROM legacy_source_records
WHERE source=$1 AND entity=ANY($2)`, im.sourceName, im.snapshotEntities())
	if err != nil {
		return err
	}
	defer rows.Close()
	current := map[string]map[int64]legacy.Record{}
	for entity, records := range im.source {
		current[entity] = index(records)
	}
	saved := map[string]map[int64]bool{}
	total := 0
	for rows.Next() {
		var entity, hash string
		var id int64
		if err = rows.Scan(&entity, &id, &hash); err != nil {
			return err
		}
		r := current[entity][id]
		if r == nil || checksum(jsonMap(archivableRecord(entity, r))) != hash {
			return fmt.Errorf("source changed: %s/%d; incremental migration requires review", entity, id)
		}
		if saved[entity] == nil {
			saved[entity] = map[int64]bool{}
		}
		saved[entity][id] = true
		total++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if total > 0 || entities != nil {
		for entity, records := range current {
			for id := range records {
				if !saved[entity][id] {
					return fmt.Errorf("source changed: %s/%d added; incremental migration requires review", entity, id)
				}
			}
		}
	}
	return nil
}

func deleted(r legacy.Record) bool {
	return (r["delete_time"] != "" && r["delete_time"] != "0") || r["is_delete"] == "1"
}

func (im *importer) recordMapped(ctx context.Context, tx pgx.Tx, entity string, r legacy.Record, target, runID int64) error {
	if err := record(ctx, tx, im.sourceName, entity, sourceID(r), runID, jsonMap(archivableRecord(entity, r))); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO legacy_id_map(source,entity,legacy_id,target_id) VALUES($1,$2,$3,$4)
ON CONFLICT(source,entity,legacy_id) DO UPDATE SET target_id=EXCLUDED.target_id`, im.sourceName, entity, sourceID(r), target)
	return err
}

func archivableRecord(entity string, r legacy.Record) legacy.Record {
	if entity != "users" || r["password"] == "" {
		return r
	}
	copy := make(legacy.Record, len(r)-1)
	for key, value := range r {
		if key != "password" {
			copy[key] = value
		}
	}
	return copy
}

func (im *importer) currencyID() string {
	for _, r := range im.source["currencies"] {
		if r["code"] == "CNY" {
			return strconv.FormatInt(sourceID(r), 10)
		}
	}
	return ""
}
