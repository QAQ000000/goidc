package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/jackc/pgx/v5"
)

type legacyConfigMaps struct {
	Groups  map[int64]int64
	Links   map[int64]int64
	Options map[int64]int64
	Subs    map[int64]int64
}

func legacyQuantityType(optionType int64) bool {
	switch optionType {
	case 4, 7, 9, 11, 14, 15, 16, 17, 18, 19:
		return true
	default:
		return false
	}
}

func legacyParts(raw string) (string, string) {
	key, label, ok := strings.Cut(raw, "|")
	if !ok {
		return strings.TrimSpace(raw), strings.TrimSpace(raw)
	}
	return strings.TrimSpace(key), strings.TrimSpace(label)
}

func legacyChoice(raw string, id int64) (string, string) {
	value, label := legacyParts(raw)
	if value == "" {
		value = "legacy-" + strconv.FormatInt(id, 10)
	}
	if before, after, ok := strings.Cut(label, "^"); ok {
		if strings.TrimSpace(after) != "" {
			label = strings.TrimSpace(after)
		} else {
			label = strings.TrimSpace(before)
		}
	}
	if label == "" {
		label = value
	}
	return limitConfigText(label), limitConfigText(value)
}

func limitConfigText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 120 {
		runes = runes[:120]
	}
	return string(runes)
}

func legacySlug(raw string, id int64, used map[string]bool) string {
	key, _ := legacyParts(raw)
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(key) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "legacy"
	}
	if len(slug) > 100 {
		slug = slug[:100]
	}
	base := slug
	if used[slug] {
		slug = fmt.Sprintf("%s-%d", base, id)
	}
	used[slug] = true
	return slug
}

func boolFlag(raw string) bool { return raw == "1" || strings.EqualFold(raw, "true") }

func intOrZero(raw string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return n
}

func configPriceFields() []struct {
	cycle string
	price string
	setup string
} {
	return []struct{ cycle, price, setup string }{
		{"hourly", "hour", "hsetupfee"},
		{"daily", "day", "dsetupfee"},
		{"monthly", "monthly", "msetupfee"},
		{"quarterly", "quarterly", "qsetupfee"},
		{"semiannually", "semiannually", "ssetupfee"},
		{"annually", "annually", "asetupfee"},
		{"biennially", "biennially", "bsetupfee"},
		{"triennially", "triennially", "tsetupfee"},
		{"once", "onetime", "osetupfee"},
	}
}

func configPrice(r legacy.Record, field string) (int64, bool, error) {
	if strings.TrimSpace(r[field]) == "" {
		return 0, false, nil
	}
	n, err := legacy.Cents(r[field])
	if err != nil {
		return 0, false, err
	}
	// Legacy -1 means that a cycle is not offered.
	if n == -100 {
		return 0, false, nil
	}
	if n < 0 {
		return 0, false, fmt.Errorf("negative configuration price %s", field)
	}
	return n, true, nil
}

func (im *importer) importProductConfiguration(ctx context.Context, tx pgx.Tx, products map[int64]int64, runID int64) (legacyConfigMaps, error) {
	out := legacyConfigMaps{Groups: map[int64]int64{}, Links: map[int64]int64{}, Options: map[int64]int64{}, Subs: map[int64]int64{}}
	if len(im.source["product_config_groups"]) == 0 {
		return out, nil
	}
	for _, r := range im.source["product_config_groups"] {
		if replayed, err := im.replay(ctx, tx, "product_config_groups", r, out.Groups); err != nil {
			return out, err
		} else if replayed {
			continue
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO legacy_product_config_groups(name,description,global_group,upstream_id,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, limitConfigText(r["name"]), r["description"], boolFlag(r["global"]), intOrZero(r["upstream_id"]), im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return out, err
		}
		out.Groups[sourceID(r)] = id
		im.report.Mapped["product_config_groups"]++
		if err := im.recordMapped(ctx, tx, "product_config_groups", r, id, runID); err != nil {
			return out, err
		}
	}
	groupProducts := map[int64]int64{}
	for _, r := range im.source["product_config_links"] {
		gid, pid := referenceID(r, "gid"), referenceID(r, "pid")
		product := products[pid]
		if product == 0 || out.Groups[gid] == 0 {
			return out, fmt.Errorf("unmapped product configuration link: %d", sourceID(r))
		}
		if previous := groupProducts[gid]; previous != 0 && previous != product {
			return out, fmt.Errorf("shared legacy configuration group requires review: %d", gid)
		}
		groupProducts[gid] = product
		if replayed, err := im.replay(ctx, tx, "product_config_links", r, out.Links); err != nil {
			return out, err
		} else if replayed {
			continue
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO legacy_product_config_links(config_group_id,product_id,legacy_source,legacy_id) VALUES($1,$2,$3,$4) RETURNING id`, out.Groups[gid], product, im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return out, err
		}
		out.Links[sourceID(r)] = id
		im.report.Mapped["product_config_links"]++
		if err := im.recordMapped(ctx, tx, "product_config_links", r, id, runID); err != nil {
			return out, err
		}
	}
	usedSlugs := map[int64]map[string]bool{}
	for _, r := range im.source["product_config_options"] {
		gid := referenceID(r, "gid")
		product := groupProducts[gid]
		legacyGroup := out.Groups[gid]
		if product == 0 || legacyGroup == 0 {
			return out, fmt.Errorf("unmapped product configuration option: %d", sourceID(r))
		}
		if replayed, err := im.replay(ctx, tx, "product_config_options", r, out.Options); err != nil {
			return out, err
		} else if replayed {
			continue
		}
		if usedSlugs[product] == nil {
			usedSlugs[product] = map[string]bool{}
		}
		_, name := legacyParts(r["option_name"])
		if name == "" {
			name = "Legacy option " + strconv.FormatInt(sourceID(r), 10)
		}
		active := !boolFlag(r["hidden"])
		required := active
		var target int64
		meta, _ := json.Marshal(r)
		if err := tx.QueryRow(ctx, `INSERT INTO product_option_groups(product_id,name,slug,required,sort_order,active,legacy_config_group_id,option_type,qty_minimum,qty_maximum,qty_stage,unit,notes,upgrade_enabled,legacy_metadata,legacy_source,legacy_id,hierarchy_native) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,false) RETURNING id`, product, limitConfigText(name), legacySlug(r["option_name"], sourceID(r), usedSlugs[product]), required, intOrZero(r["order"]), active, legacyGroup, intOrZero(r["option_type"]), intOrZero(r["qty_minimum"]), intOrZero(r["qty_maximum"]), intOrZero(r["qty_stage"]), limitConfigText(r["unit"]), r["notes"], boolFlag(r["upgrade"]), meta, im.sourceName, sourceID(r)).Scan(&target); err != nil {
			return out, err
		}
		out.Options[sourceID(r)] = target
		im.report.Mapped["product_config_options"]++
		if err := im.recordMapped(ctx, tx, "product_config_options", r, target, runID); err != nil {
			return out, err
		}
	}
	prices := map[int64]legacy.Record{}
	for _, r := range im.source["product_config_prices"] {
		prices[referenceID(r, "relid")] = r
	}
	usedValues := map[int64]map[string]bool{}
	quantityCanonical := map[int64]int64{}
	quantityGroups := map[int64]bool{}
	quantityBounds := map[int64][2]int64{}
	for _, r := range im.source["product_config_options"] {
		id := sourceID(r)
		quantityGroups[id] = legacyQuantityType(intOrZero(r["option_type"]))
		quantityBounds[id] = [2]int64{intOrZero(r["qty_minimum"]), intOrZero(r["qty_maximum"])}
	}
	for _, r := range im.source["product_config_option_subs"] {
		parent := referenceID(r, "config_id")
		groupID := out.Options[parent]
		if groupID == 0 {
			return out, fmt.Errorf("unmapped product config sub option: %d", sourceID(r))
		}
		quantity := quantityGroups[parent]
		if quantity {
			replayed, err := im.replayQuantitySub(ctx, tx, r, out.Subs)
			if err != nil {
				return out, err
			}
			if replayed {
				continue
			}
		} else if replayed, err := im.replay(ctx, tx, "product_config_option_subs", r, out.Subs); err != nil {
			return out, err
		} else if replayed {
			continue
		}
		if usedValues[groupID] == nil {
			usedValues[groupID] = map[string]bool{}
		}
		label, value := legacyChoice(r["option_name"], sourceID(r))
		if usedValues[groupID][value] {
			value = limitConfigText(value + "~" + strconv.FormatInt(sourceID(r), 10))
		}
		usedValues[groupID][value] = true
		priceRow := prices[sourceID(r)]
		if quantity {
			canonical := quantityCanonical[groupID]
			if canonical == 0 {
				meta, _ := json.Marshal(map[string]any{"quantity_option": true, "first_legacy_sub_id": sourceID(r)})
				minimum, maximum := quantityRange(r["qty_minimum"], r["qty_maximum"])
				bounds := quantityBounds[parent]
				groupMinimum, groupMaximum := bounds[0], bounds[1]
				if groupMinimum > 0 {
					minimum = groupMinimum
				}
				if groupMaximum > 0 {
					maximum = groupMaximum
				}
				active := !boolFlag(r["hidden"])
				if err := tx.QueryRow(ctx, `INSERT INTO product_options(group_id,label,value,price_delta_cents,setup_delta_cents,sort_order,active,qty_minimum,qty_maximum,hidden,upgrade_enabled,legacy_metadata,legacy_source,legacy_id) VALUES($1,$2,$3,0,0,$4,$5,$6,$7,$8,true,$9,$10,$11) RETURNING id`, groupID, label, "legacy-quantity-"+strconv.FormatInt(parent, 10), intOrZero(r["sort_order"]), active, minimum, maximum, !active, meta, im.sourceName, sourceID(r)).Scan(&canonical); err != nil {
					return out, err
				}
				quantityCanonical[groupID] = canonical
			} else if !boolFlag(r["hidden"]) {
				if _, err := tx.Exec(ctx, "UPDATE product_options SET active=true,hidden=false,updated_at=now() WHERE id=$1", canonical); err != nil {
					return out, err
				}
			}
			canonical = quantityCanonical[groupID]
			out.Subs[sourceID(r)] = canonical
			min, max := quantityRange(r["qty_minimum"], r["qty_maximum"])
			if _, err := tx.Exec(ctx, `INSERT INTO legacy_product_config_option_aliases(option_id,legacy_source,legacy_id,qty_minimum,qty_maximum) VALUES($1,$2,$3,$4,$5) ON CONFLICT(legacy_source,legacy_id) DO UPDATE SET option_id=EXCLUDED.option_id,qty_minimum=EXCLUDED.qty_minimum,qty_maximum=EXCLUDED.qty_maximum`, canonical, im.sourceName, sourceID(r), min, max); err != nil {
				return out, err
			}
			im.report.Mapped["product_config_option_subs"]++
			if err := im.recordMapped(ctx, tx, "product_config_option_subs", r, canonical, runID); err != nil {
				return out, err
			}
			if priceRow != nil {
				if err := im.importOptionPriceTiers(ctx, tx, canonical, r, priceRow); err != nil {
					return out, err
				}
				im.report.Mapped["product_config_prices"]++
			}
			continue
		}
		monthly, setup, monthlyOK, err := int64(0), int64(0), false, error(nil)
		if priceRow != nil {
			for _, field := range configPriceFields() {
				monthly, monthlyOK, err = configPrice(priceRow, field.price)
				if err != nil {
					return out, err
				}
				if !monthlyOK {
					continue
				}
				setup, _, err = configPrice(priceRow, field.setup)
				if err != nil {
					return out, err
				}
				break
			}
		}
		meta, _ := json.Marshal(r)
		active := !boolFlag(r["hidden"])
		var target int64
		if err := tx.QueryRow(ctx, `INSERT INTO product_options(group_id,label,value,price_delta_cents,setup_delta_cents,sort_order,active,qty_minimum,qty_maximum,hidden,upgrade_enabled,legacy_metadata,legacy_source,legacy_id,hierarchy_native) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,false) RETURNING id`, groupID, label, value, monthly, setup, intOrZero(r["sort_order"]), active, intOrZero(r["qty_minimum"]), intOrZero(r["qty_maximum"]), !active, true, meta, im.sourceName, sourceID(r)).Scan(&target); err != nil {
			return out, err
		}
		out.Subs[sourceID(r)] = target
		im.report.Mapped["product_config_option_subs"]++
		if err := im.recordMapped(ctx, tx, "product_config_option_subs", r, target, runID); err != nil {
			return out, err
		}
		if priceRow != nil {
			if err := im.importOptionPrices(ctx, tx, target, priceRow, runID); err != nil {
				return out, err
			}
			im.report.Mapped["product_config_prices"]++
		}
	}
	return out, nil
}

func (im *importer) importOptionPrices(ctx context.Context, tx pgx.Tx, optionID int64, source legacy.Record, runID int64) error {
	var mapped int64
	for _, field := range configPriceFields() {
		price, ok, err := configPrice(source, field.price)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		setup, _, err := configPrice(source, field.setup)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO product_option_prices(option_id,billing_cycle,price_cents,setup_fee_cents,active,legacy_source,legacy_id) VALUES($1,$2,$3,$4,true,$5,$6) ON CONFLICT(option_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=true,updated_at=now() RETURNING id`, optionID, field.cycle, price, setup, im.sourceName, sourceID(source)).Scan(&mapped); err != nil {
			return err
		}
	}
	_ = mapped
	return nil
}

func quantityRange(minRaw, maxRaw string) (int64, int64) {
	minimum, maximum := intOrZero(minRaw), intOrZero(maxRaw)
	if minimum < 1 {
		minimum = 1
	}
	if maximum <= 0 {
		maximum = 1000000
	} else if maximum < minimum {
		maximum = minimum
	}
	return minimum, maximum
}

func (im *importer) importOptionPriceTiers(ctx context.Context, tx pgx.Tx, optionID int64, sub, source legacy.Record) error {
	minimum, maximum := quantityRange(sub["qty_minimum"], sub["qty_maximum"])
	for _, field := range configPriceFields() {
		price, ok, err := configPrice(source, field.price)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		setup, _, err := configPrice(source, field.setup)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO product_option_price_tiers(option_id,billing_cycle,qty_minimum,qty_maximum,price_cents,setup_fee_cents,active,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6,true,$7,$8) ON CONFLICT(option_id,billing_cycle,qty_minimum,qty_maximum) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=true,updated_at=now()`, optionID, field.cycle, minimum, maximum, price, setup, im.sourceName, sourceID(source)); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) replayQuantitySub(ctx context.Context, tx pgx.Tx, r legacy.Record, out map[int64]int64) (bool, error) {
	legacyID := sourceID(r)
	var target int64
	err := tx.QueryRow(ctx, "SELECT target_id FROM legacy_id_map WHERE source=$1 AND entity='product_config_option_subs' AND legacy_id=$2", im.sourceName, legacyID).Scan(&target)
	fromAlias := false
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT option_id FROM legacy_product_config_option_aliases WHERE legacy_source=$1 AND legacy_id=$2", im.sourceName, legacyID).Scan(&target)
		fromAlias = err == nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fromAlias {
		var archived bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1 AND entity='product_config_option_subs' AND legacy_id=$2)", im.sourceName, legacyID).Scan(&archived); err != nil {
			return false, err
		}
		if !archived {
			return false, fmt.Errorf("unverified existing target: product_config_option_subs/%d", legacyID)
		}
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM product_options WHERE id=$1)", target).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("stale target mapping: product_config_option_subs/%d", legacyID)
	}
	out[legacyID] = target
	im.report.Replayed["product_config_option_subs"]++
	return true, nil
}

func (im *importer) importServiceOptionSelections(ctx context.Context, tx pgx.Tx, services map[int64]int64, configs legacyConfigMaps, runID int64) error {
	for _, r := range im.source["service_option_selections"] {
		if replayed, err := im.replay(ctx, tx, "service_option_selections", r, map[int64]int64{}); err != nil {
			return err
		} else if replayed {
			continue
		}
		service, group, option := services[referenceID(r, "relid")], configs.Options[referenceID(r, "configid")], configs.Subs[referenceID(r, "optionid")]
		if service == 0 || group == 0 {
			return fmt.Errorf("unmapped service configuration selection: %d", sourceID(r))
		}
		var target int64
		var optionValue any
		if option > 0 {
			optionValue = option
		}
		if err := tx.QueryRow(ctx, `INSERT INTO service_option_selections(service_id,group_id,option_id,quantity,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(legacy_source,legacy_id) DO UPDATE SET quantity=EXCLUDED.quantity RETURNING id`, service, group, optionValue, intOrZero(r["qty"]), im.sourceName, sourceID(r)).Scan(&target); err != nil {
			return err
		}
		im.report.Mapped["service_option_selections"]++
		if err := im.recordMapped(ctx, tx, "service_option_selections", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) importProductConfigurationOptionLinks(ctx context.Context, tx pgx.Tx, configs legacyConfigMaps, runID int64) error {
	for _, r := range im.source["product_config_option_links"] {
		mapped := map[int64]int64{}
		if replayed, err := im.replay(ctx, tx, "product_config_option_links", r, mapped); err != nil {
			return err
		} else if replayed {
			continue
		}
		group := configs.Options[referenceID(r, "config_id")]
		if group == 0 {
			return fmt.Errorf("unmapped product configuration link: %d", sourceID(r))
		}
		meta, _ := json.Marshal(r)
		var target int64
		if err := tx.QueryRow(ctx, `INSERT INTO legacy_product_config_option_links(config_option_group_id,sub_ids,relation,link_type,relation_legacy_id,upstream_id,legacy_metadata,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, group, r["sub_id"], r["relation"], r["type"], referenceID(r, "relation_id"), referenceID(r, "upstream_id"), meta, im.sourceName, sourceID(r)).Scan(&target); err != nil {
			return err
		}
		im.report.Mapped["product_config_option_links"]++
		if err := im.recordMapped(ctx, tx, "product_config_option_links", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}
