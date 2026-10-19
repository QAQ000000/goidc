package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

const (
	supplierCatalogImportLimit = 50
	supplierCatalogReadTimeout = 25 * time.Second
)

// SupplierCatalogConfiguration is the immutable import-time provider schema and
// ID map. Purchase-time choices are frozen separately on each service.
type SupplierCatalogConfiguration struct {
	Product       supplier.Product                       `json:"product"`
	MultiplierBPS int64                                  `json:"multiplier_bps"`
	Options       map[int64]SupplierCatalogOptionMapping `json:"options"`
	Fields        map[string]int64                       `json:"fields"`
}

type SupplierCatalogOptionMapping struct {
	OptionID int64 `json:"option_id"`
	ChoiceID int64 `json:"choice_id"`
}

type SupplierCatalogImportItem struct {
	UpstreamProductID        int64  `json:"upstream_product_id"`
	ExpectedUpstreamRevision string `json:"expected_upstream_revision,omitempty"`
	Name                     string `json:"name,omitempty"`
}

type SupplierCatalogImportInput struct {
	ExpectedSupplierVersion int64                       `json:"expected_supplier_version"`
	CategoryID              *int64                      `json:"category_id"`
	PriceMultiplierBPS      int64                       `json:"price_multiplier_bps"`
	Active                  bool                        `json:"active"`
	Items                   []SupplierCatalogImportItem `json:"items"`
}

type SupplierCatalogImportPreviewItem struct {
	Product     supplier.Product          `json:"product"`
	Slug        string                    `json:"slug"`
	LocalPrices map[string]supplier.Price `json:"local_prices"`
	Importable  bool                      `json:"importable"`
	Reason      string                    `json:"reason,omitempty"`
}

type SupplierCatalogImportPreview struct {
	ExpectedSupplierVersion int64                              `json:"expected_supplier_version"`
	CategoryID              *int64                             `json:"category_id"`
	PriceMultiplierBPS      int64                              `json:"price_multiplier_bps"`
	Active                  bool                               `json:"active"`
	Items                   []SupplierCatalogImportPreviewItem `json:"items"`
}

type SupplierCatalogImportResultItem struct {
	UpstreamProductID int64  `json:"upstream_product_id"`
	ProductID         int64  `json:"product_id"`
	Name              string `json:"name"`
	Slug              string `json:"slug"`
	Status            string `json:"status"`
}

type SupplierCatalogImportResult struct {
	Items []SupplierCatalogImportResultItem `json:"items"`
}

// SupplierMarkupCents rounds a percentage-scaled component once to integer
// cents. Bounds keep multiplication safe and match local catalog money limits.
func SupplierMarkupCents(cents, bps int64) (int64, error) {
	if cents < 0 || cents > 100000000 || bps < 1 || bps > 1000000 {
		return 0, conflict("invalid_supplier_markup", "售价比例或上游金额超出允许范围")
	}
	value := (cents*bps + 5000) / 10000
	if value > 100000000 {
		return 0, conflict("supplier_markup_overflow", "按比例计算的售价超出允许范围")
	}
	return value, nil
}

func normalizeSupplierCatalogImport(in SupplierCatalogImportInput, confirm bool) (SupplierCatalogImportInput, error) {
	if in.ExpectedSupplierVersion < 1 || in.CategoryID != nil && *in.CategoryID < 1 || len(in.Items) < 1 || len(in.Items) > supplierCatalogImportLimit || in.PriceMultiplierBPS < 1 || in.PriceMultiplierBPS > 1000000 {
		return in, conflict("invalid_supplier_import", "请选择 1-50 个商品、有效分类与 0.01%-10000% 的售价比例")
	}
	seen := map[int64]bool{}
	for i := range in.Items {
		item := &in.Items[i]
		item.Name = strings.TrimSpace(item.Name)
		item.ExpectedUpstreamRevision = strings.TrimSpace(item.ExpectedUpstreamRevision)
		if item.UpstreamProductID < 1 || seen[item.UpstreamProductID] || item.Name != "" && !contentTextValid(item.Name, 180, false) || len(item.ExpectedUpstreamRevision) > 128 || confirm && item.ExpectedUpstreamRevision == "" {
			return in, conflict("invalid_supplier_import", "商品编号、名称或预览版本无效，请重新预览")
		}
		seen[item.UpstreamProductID] = true
	}
	return in, nil
}

func supplierCatalogImportSlug(supplierID, productID int64) string {
	return fmt.Sprintf("supplier-%d-%d", supplierID, productID)
}

func supplierCatalogCycles(p supplier.Product) []string {
	cycles := []string{}
	for _, cycle := range supplier.BillingCycles() {
		if _, ok := p.Prices[cycle]; ok {
			cycles = append(cycles, cycle)
		}
	}
	return cycles
}

func supplierCatalogScaledPrice(price supplier.Price, bps int64) (supplier.Price, error) {
	amount, err := SupplierMarkupCents(price.AmountCents, bps)
	if err != nil {
		return supplier.Price{}, err
	}
	setup, err := SupplierMarkupCents(price.SetupCents, bps)
	return supplier.Price{AmountCents: amount, SetupCents: setup}, err
}

func supplierCatalogPurchaseField(field supplier.Field, index int) (PurchaseField, error) {
	f := PurchaseField{Key: "supplier_" + strconv.FormatInt(field.ID, 10), Label: field.Name, Description: field.Description, Required: field.Required, Active: true, MaxLength: 2048, Choices: []string{}, SortOrder: index, Scope: "purchase", Pattern: field.Pattern}
	switch field.Type {
	case "text", "textarea":
		f.Type = field.Type
	case "password":
		f.Type, f.Encrypted = "text", true
	case "dropdown":
		f.Type, f.Choices = "select", append([]string{}, field.Choices...)
	case "link":
		f.Type = "url"
	case "tickbox":
		f.Type = "checkbox"
	default:
		return f, conflict("unsupported_field_type", "上游购买字段类型暂不支持")
	}
	return f, normalizePurchaseField(&f)
}

// Validate the complete schema before any insert. A single unsupported item
// cannot leave a partly imported batch or a purchasable but undeliverable item.
func (s *Store) supplierCatalogImportable(p supplier.Product, bps int64) (map[string]supplier.Price, string) {
	prices := map[string]supplier.Price{}
	if !p.Supported {
		if p.UnsupportedReason != "" {
			return prices, p.UnsupportedReason
		}
		return prices, "unsupported_product"
	}
	if p.ID < 1 || p.Revision == "" || p.Currency != "CNY" || len(p.Prices) == 0 || len(supplierCatalogCycles(p)) != len(p.Prices) {
		return prices, "unsupported_currency_or_cycle"
	}
	if !contentTextValid(p.Name, 180, false) || p.Name == "" || !contentTextValid(p.Description, 10000, true) || p.Stock != nil && (*p.Stock < 0 || *p.Stock > 2147483647) {
		return prices, "invalid_product_schema"
	}
	for cycle, price := range p.Prices {
		scaled, err := supplierCatalogScaledPrice(price, bps)
		if err != nil || price.AmountCents+price.SetupCents > 100000000 {
			return prices, "supplier_markup_overflow"
		}
		prices[cycle] = scaled
	}
	if len(p.Options) > 100 || len(p.Fields) > 20 {
		return prices, "supplier_configuration_limit"
	}
	seenOptions := map[int64]bool{}
	for _, option := range p.Options {
		if option.ID < 1 || seenOptions[option.ID] || !contentTextValid(option.Name, 120, false) || option.Name == "" || len(option.Choices) == 0 || len(option.Choices) > 100 || option.Type != "select" && option.Type != "quantity" || option.Type == "quantity" && len(option.Choices) != 1 {
			return prices, "unsupported_option_type"
		}
		seenOptions[option.ID] = true
		if option.Type == "quantity" && option.Choices[0].MaxQty <= 0 {
			// Local maximum=0 means unbounded. A provider's zero-only interval
			// cannot use that representation without admitting extra quantities.
			return prices, "invalid_quantity_bounds"
		}
		seenChoices := map[int64]bool{}
		for _, choice := range option.Choices {
			if choice.ID < 1 || seenChoices[choice.ID] || choice.Name == "" || !contentTextValid(choice.Name, 120, false) || len(choice.Prices) != len(p.Prices) || option.Type == "quantity" && (choice.MinQty < 0 || choice.MaxQty < choice.MinQty || choice.MaxQty > 1000000) {
				return prices, "invalid_option_schema"
			}
			seenChoices[choice.ID] = true
			for cycle := range p.Prices {
				price, ok := choice.Prices[cycle]
				if _, err := supplierCatalogScaledPrice(price, bps); !ok || err != nil {
					return prices, "option_price_missing"
				}
				if option.Type == "quantity" && (option.QuantityMode == "graduated" || option.QuantityMode == "volume") {
					tiers := choice.PriceTiers[cycle]
					if len(tiers) == 0 || len(tiers) > 100 {
						return prices, "invalid_option_price_tiers"
					}
					next := int64(1)
					for _, tier := range tiers {
						if tier.MinQty != next || tier.MaxQty < tier.MinQty || tier.MaxQty > choice.MaxQty {
							return prices, "invalid_option_price_tiers"
						}
						if _, err := supplierCatalogScaledPrice(supplier.Price{AmountCents: tier.AmountCents, SetupCents: tier.SetupCents}, bps); err != nil {
							return prices, "supplier_markup_overflow"
						}
						next = tier.MaxQty + 1
					}
					if next != choice.MaxQty+1 {
						return prices, "invalid_option_price_tiers"
					}
				}
			}
		}
	}
	seenFields := map[int64]bool{}
	for i, field := range p.Fields {
		if field.ID < 1 || seenFields[field.ID] {
			return prices, "field_schema_invalid"
		}
		seenFields[field.ID] = true
		f, err := supplierCatalogPurchaseField(field, i)
		if err != nil {
			return prices, "unsupported_field_schema"
		}
		if f.Encrypted && len(s.PurchaseFieldEncryptionKey) != 32 {
			return prices, "purchase_field_encryption_unconfigured"
		}
	}
	return prices, ""
}

func (s *Store) supplierCatalogImportTx(ctx context.Context, actor int64, supplierID int64, in SupplierCatalogImportInput) (pgx.Tx, error) {
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return nil, err
	}
	if err = CheckAdminCreationPermission(ctx, tx, actor, "products"); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func (s *Store) checkSupplierCatalogImport(ctx context.Context, tx pgx.Tx, supplierID int64, in SupplierCatalogImportInput) (supplier.Adapter, error) {
	var version int64
	var active bool
	if err := tx.QueryRow(ctx, "SELECT version,active FROM supplier_accounts WHERE id=$1 FOR SHARE", supplierID).Scan(&version, &active); err != nil {
		return nil, err
	}
	if version != in.ExpectedSupplierVersion || !active {
		return nil, conflict("version_conflict", "供应商已停用或配置已变更，请重新预览")
	}
	a, _, err := s.supplierAdapterWith(ctx, tx, supplierID)
	if err != nil {
		return nil, err
	}
	if !a.Capabilities().Product || !a.Capabilities().Create {
		return nil, conflict("supplier_capability_unsupported", "供应商不支持商品导入与交付")
	}
	if in.CategoryID != nil {
		var id int64
		if err := tx.QueryRow(ctx, "SELECT id FROM product_categories WHERE id=$1 FOR SHARE", *in.CategoryID).Scan(&id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, conflict("invalid_catalog_category", "本地分类已不存在，请重新选择")
			}
			return nil, err
		}
	}
	return a, nil
}

func (s *Store) readSupplierCatalogImport(ctx context.Context, a supplier.Adapter, supplierID int64, in SupplierCatalogImportInput, timeout time.Duration) (SupplierCatalogImportPreview, error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out := SupplierCatalogImportPreview{ExpectedSupplierVersion: in.ExpectedSupplierVersion, CategoryID: in.CategoryID, PriceMultiplierBPS: in.PriceMultiplierBPS, Active: in.Active, Items: make([]SupplierCatalogImportPreviewItem, len(in.Items))}
	var workers sync.WaitGroup
	jobs := make(chan int)
	for n := 0; n < 4; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				selected := in.Items[i]
				p, err := a.Product(readCtx, selected.UpstreamProductID)
				item := SupplierCatalogImportPreviewItem{Product: p, Slug: supplierCatalogImportSlug(supplierID, selected.UpstreamProductID), LocalPrices: map[string]supplier.Price{}}
				if err != nil {
					item.Product.ID = selected.UpstreamProductID
					item.Reason = "supplier_product_read_failed"
				} else if p.ID != selected.UpstreamProductID {
					item.Reason = "supplier_product_identity_mismatch"
				} else {
					if selected.Name != "" {
						item.Product.Name = selected.Name
					}
					item.LocalPrices, item.Reason = s.supplierCatalogImportable(item.Product, in.PriceMultiplierBPS)
					item.Importable = item.Reason == ""
				}
				out.Items[i] = item
			}
		}()
	}
	for i := range in.Items {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if readCtx.Err() != nil {
		return out, conflict("supplier_catalog_timeout", "读取上游商品超过 25 秒，请缩小批次后重试；本批未创建商品")
	}
	return out, nil
}

func (s *Store) PreviewSupplierCatalogImport(ctx context.Context, actor, supplierID int64, input SupplierCatalogImportInput) (SupplierCatalogImportPreview, error) {
	var out SupplierCatalogImportPreview
	in, err := normalizeSupplierCatalogImport(input, false)
	if err != nil {
		return out, err
	}
	tx, err := s.supplierCatalogImportTx(ctx, actor, supplierID, in)
	if err != nil {
		return out, err
	}
	a, err := s.checkSupplierCatalogImport(ctx, tx, supplierID, in)
	if err != nil {
		tx.Rollback(ctx)
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out, err = s.readSupplierCatalogImport(ctx, a, supplierID, in, supplierCatalogReadTimeout)
	if err != nil {
		return out, err
	}
	// A permission, credential or account change during upstream reads must
	// invalidate the preview too; never label an obsolete account as ready.
	tx, err = s.supplierCatalogImportTx(ctx, actor, supplierID, in)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = s.checkSupplierCatalogImport(ctx, tx, supplierID, in); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ImportSupplierCatalog(ctx context.Context, actor, supplierID int64, key string, input SupplierCatalogImportInput) (SupplierCatalogImportResult, error) {
	return s.importSupplierCatalog(ctx, actor, supplierID, key, input, supplierCatalogReadTimeout)
}

func (s *Store) importSupplierCatalog(ctx context.Context, actor, supplierID int64, key string, input SupplierCatalogImportInput, readTimeout time.Duration) (SupplierCatalogImportResult, error) {
	out := SupplierCatalogImportResult{Items: []SupplierCatalogImportResultItem{}}
	in, err := normalizeSupplierCatalogImport(input, true)
	if err != nil {
		return out, err
	}
	if len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_idempotency_key", "导入请求必须提供 8-128 位幂等键")
	}
	scope := fmt.Sprintf("supplier.catalog.import.%d", supplierID)
	tx, err := s.supplierCatalogImportTx(ctx, actor, supplierID, in)
	if err != nil {
		return out, err
	}
	_, replay, err := CreationReceipt(ctx, tx, actor, scope, key, in, &out)
	if err != nil || replay {
		if err != nil {
			tx.Rollback(ctx)
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	a, err := s.checkSupplierCatalogImport(ctx, tx, supplierID, in)
	if err != nil {
		tx.Rollback(ctx)
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	preview, err := s.readSupplierCatalogImport(ctx, a, supplierID, in, readTimeout)
	if err != nil {
		return out, err
	}
	for i, item := range preview.Items {
		if !item.Importable {
			return out, conflict("supplier_batch_not_importable", fmt.Sprintf("上游商品 #%d 无法导入：%s；本批未创建商品", in.Items[i].UpstreamProductID, item.Reason))
		}
		if item.Product.Revision != in.Items[i].ExpectedUpstreamRevision {
			return out, conflict("supplier_product_changed", "上游商品价格或规格已变更，请重新预览；本批未创建商品")
		}
	}
	tx, err = s.supplierCatalogImportTx(ctx, actor, supplierID, in)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	hash, replay, err := CreationReceipt(ctx, tx, actor, scope, key, in, &out)
	if err != nil {
		return out, err
	}
	if replay {
		return out, tx.Commit(ctx)
	}
	if _, err = s.checkSupplierCatalogImport(ctx, tx, supplierID, in); err != nil {
		return out, err
	}
	for _, item := range preview.Items {
		result, err := s.insertSupplierCatalogProduct(ctx, tx, actor, supplierID, in, item)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, result)
	}
	if err = SaveCreationReceipt(ctx, tx, actor, scope, key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) insertSupplierCatalogProduct(ctx context.Context, tx pgx.Tx, actor, supplierID int64, in SupplierCatalogImportInput, item SupplierCatalogImportPreviewItem) (SupplierCatalogImportResultItem, error) {
	p := item.Product
	out := SupplierCatalogImportResultItem{UpstreamProductID: p.ID, Name: p.Name, Slug: item.Slug, Status: "created"}
	err := tx.QueryRow(ctx, `SELECT p.id,p.name,p.slug FROM supplier_product_bindings b
 JOIN supplier_catalog_imports ci ON ci.binding_id=b.id JOIN products p ON p.id=b.product_id
 WHERE b.supplier_id=$1 AND b.upstream_product_id=$2 ORDER BY p.id LIMIT 1`, supplierID, p.ID).Scan(&out.ProductID, &out.Name, &out.Slug)
	if err == nil {
		out.Status = "existing"
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var slugExists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM products WHERE slug=$1)", item.Slug).Scan(&slugExists); err != nil {
		return out, err
	}
	if slugExists {
		return out, conflict("supplier_import_slug_conflict", "自动商品标识已被其他商品占用；本批未创建商品")
	}
	cycle := supplierCatalogCycles(p)[0]
	price := item.LocalPrices[cycle]
	if err = tx.QueryRow(ctx, `INSERT INTO products(name,slug,description,category_id,billing_cycle,price_cents,setup_fee_cents,stock,active,provision_mode)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,false,'manual') RETURNING id`, p.Name, item.Slug, p.Description, in.CategoryID, cycle, price.AmountCents, price.SetupCents, p.Stock).Scan(&out.ProductID); err != nil {
		return out, err
	}
	sealed, err := supplierSeal(s.ProvisionEncryptionKey, "binding-fields", out.ProductID, map[int64]string{})
	if err != nil {
		return out, err
	}
	var provider string
	if err = tx.QueryRow(ctx, "SELECT provider FROM supplier_accounts WHERE id=$1", supplierID).Scan(&provider); err != nil {
		return out, err
	}
	var bindingID int64
	base := p.Prices[cycle]
	if err = tx.QueryRow(ctx, `INSERT INTO supplier_product_bindings(product_id,supplier_id,provider,upstream_product_id,upstream_revision,billing_cycle,currency,maximum_cost_cents,config_options,custom_fields_encrypted)
 VALUES($1,$2,$3,$4,$5,$6,'CNY',$7,'{}',$8) RETURNING id`, out.ProductID, supplierID, provider, p.ID, p.Revision, cycle, base.AmountCents+base.SetupCents, sealed).Scan(&bindingID); err != nil {
		return out, err
	}
	configuration := SupplierCatalogConfiguration{Product: p, MultiplierBPS: in.PriceMultiplierBPS, Options: map[int64]SupplierCatalogOptionMapping{}, Fields: map[string]int64{}}
	if err = s.insertSupplierCatalogOptions(ctx, tx, out.ProductID, &configuration); err != nil {
		return out, err
	}
	for i, field := range p.Fields {
		f, e := supplierCatalogPurchaseField(field, i)
		if e != nil {
			return out, e
		}
		if _, err = tx.Exec(ctx, `INSERT INTO product_purchase_fields(product_id,field_key,label,field_type,description,required,active,max_length,validation_pattern,choices,sort_order,encrypted)
 VALUES($1,$2,$3,$4,$5,$6,true,$7,$8,$9,$10,$11)`, out.ProductID, f.Key, f.Label, f.Type, f.Description, f.Required, f.MaxLength, f.Pattern, f.Choices, f.SortOrder, f.Encrypted); err != nil {
			return out, err
		}
		configuration.Fields[f.Key] = field.ID
	}
	raw, err := json.Marshal(configuration)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO supplier_catalog_imports(binding_id,configuration) VALUES($1,$2)", bindingID, raw); err != nil {
		return out, err
	}
	cycles := supplierCatalogCycles(p)
	for _, c := range cycles {
		v := item.LocalPrices[c]
		if _, err = tx.Exec(ctx, `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,$2,$3,$4,true)
 ON CONFLICT(product_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=true`, out.ProductID, c, v.AmountCents, v.SetupCents); err != nil {
			return out, err
		}
	}
	snapshot, _ := json.Marshal(supplierSnapshot{SupplierID: supplierID, BindingID: bindingID, Provider: provider})
	if _, err = tx.Exec(ctx, "UPDATE products SET provision_mode='supplier',delivery_config=$2,active=$3 WHERE id=$1", out.ProductID, snapshot, in.Active); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "supplier.catalog.product.imported", "product", out.ProductID, map[string]any{"supplier_id": supplierID, "binding_id": bindingID, "upstream_product_id": p.ID, "upstream_revision": p.Revision, "multiplier_bps": in.PriceMultiplierBPS, "category_id": in.CategoryID, "active": in.Active, "billing_cycles": cycles}); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Store) insertSupplierCatalogOptions(ctx context.Context, tx pgx.Tx, productID int64, configuration *SupplierCatalogConfiguration) error {
	cycles := supplierCatalogCycles(configuration.Product)
	for i, option := range configuration.Product.Options {
		var groupID int64
		optionType, minQty, maxQty, stage := int64(1), int64(0), int64(0), int64(0)
		if option.Type == "quantity" {
			optionType, minQty, maxQty, stage = 4, option.Choices[0].MinQty, option.Choices[0].MaxQty, 1
		}
		if err := tx.QueryRow(ctx, `INSERT INTO product_option_groups(product_id,name,slug,required,sort_order,active,option_type,qty_minimum,qty_maximum,qty_stage)
 VALUES($1,$2,$3,$4,$5,true,$6,$7,$8,$9) RETURNING id`, productID, option.Name, fmt.Sprintf("supplier-%d", option.ID), option.Required, i, optionType, minQty, maxQty, stage).Scan(&groupID); err != nil {
			return err
		}
		for j, choice := range option.Choices {
			var optionID int64
			if err := tx.QueryRow(ctx, `INSERT INTO product_options(group_id,label,value,sort_order,active,qty_minimum,qty_maximum)
 VALUES($1,$2,$3,$4,true,$5,$6) RETURNING id`, groupID, choice.Name, strconv.FormatInt(choice.ID, 10), j, choice.MinQty, choice.MaxQty).Scan(&optionID); err != nil {
				return err
			}
			configuration.Options[optionID] = SupplierCatalogOptionMapping{OptionID: option.ID, ChoiceID: choice.ID}
			for _, cycle := range cycles {
				v, err := supplierCatalogScaledPrice(choice.Prices[cycle], configuration.MultiplierBPS)
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO product_option_prices(option_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,$2,$3,$4,true)`, optionID, cycle, v.AmountCents, v.SetupCents); err != nil {
					return err
				}
				tiers := choice.PriceTiers[cycle]
				// The ordinary local linear fallback multiplies setup per unit.
				// Provider quantity setup is charged once, so encode even a
				// linear source as one tier shared by Go and browser pricing.
				if option.Type == "quantity" && len(tiers) == 0 && choice.MaxQty > 0 {
					base := choice.Prices[cycle]
					tiers = []supplier.PriceTier{{MinQty: 1, MaxQty: choice.MaxQty, AmountCents: base.AmountCents, SetupCents: base.SetupCents}}
				}
				for _, tier := range tiers {
					price, err := supplierCatalogScaledPrice(supplier.Price{AmountCents: tier.AmountCents, SetupCents: tier.SetupCents}, configuration.MultiplierBPS)
					if err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `INSERT INTO product_option_price_tiers(option_id,billing_cycle,qty_minimum,qty_maximum,price_cents,setup_fee_cents,active,pricing_mode) VALUES($1,$2,$3,$4,$5,$6,true,$7)`, optionID, cycle, tier.MinQty, tier.MaxQty, price.AmountCents, price.SetupCents, supplierLocalTierMode(option.QuantityMode)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func supplierLocalTierMode(mode string) string {
	if mode == "volume" {
		return "volume"
	}
	return "graduated"
}
