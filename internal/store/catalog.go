package store

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// Readers take this shared lock before product locks; management takes it exclusively.
// This keeps category visibility stable through cart mutations and checkout.
func CatalogReadLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108197)")
	return err
}
func (s *Store) CatalogTx(ctx context.Context, actor int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err == nil {
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108197)")
	}
	if err == nil {
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'products.view'=ANY(r.permissions) AND 'products.manage'=ANY(r.permissions)),false) FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actor).Scan(&allowed)
		if err == nil && !allowed {
			err = conflict("permission_denied", "没有管理商品目录的权限")
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

type CatalogGroupInput struct {
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	Description     string `json:"description"`
	Headline        string `json:"headline"`
	Tagline         string `json:"tagline"`
	SectionID       *int64 `json:"section_id"`
	Active          *bool  `json:"active"`
	SortOrder       int    `json:"sort_order"`
	ExpectedVersion int64  `json:"expected_version"`
}

func CatalogGroupTable(kind string) string {
	if kind == "section" {
		return "product_sections"
	}
	if kind == "category" {
		return "product_categories"
	}
	return ""
}
func (s *Store) SaveCatalogGroup(ctx context.Context, actor, id int64, kind string, in CatalogGroupInput, keys ...string) (int64, error) {
	table := CatalogGroupTable(kind)
	in.Name, in.Slug, in.Description, in.Headline, in.Tagline = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Slug)), strings.TrimSpace(in.Description), strings.TrimSpace(in.Headline), strings.TrimSpace(in.Tagline)
	if table == "" || in.Name == "" || !contentTextValid(in.Name, 255, false) || !contentSlug.MatchString(in.Slug) || !contentTextValid(in.Description, 1000, true) || !contentTextValid(in.Headline, 255, false) || !contentTextValid(in.Tagline, 255, false) || in.Active == nil || in.SortOrder < 0 || in.SortOrder > 1000000 || (kind == "category" && (in.SectionID == nil || *in.SectionID < 1)) || (kind == "section" && (in.SectionID != nil || in.Headline != "" || in.Tagline != "")) {
		return 0, conflict("invalid_catalog_group", "分组名称、标识、上级分组或显示状态无效")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	key := ""
	if id == 0 && len(keys) > 0 {
		key = keys[0]
	}
	if err = CheckAdminCreationPermission(ctx, tx, actor, "products"); err != nil {
		return 0, err
	}
	receipt := struct {
		ID int64 `json:"id"`
	}{}
	hash, replay, err := CreationReceipt(ctx, tx, actor, "catalog_group.create."+kind, key, in, &receipt)
	if err != nil {
		return 0, err
	}
	if replay {
		return receipt.ID, tx.Commit(ctx)
	}
	var version int64
	if id != 0 {
		if err = tx.QueryRow(ctx, "SELECT version FROM "+table+" WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
			return 0, err
		}
		if version != in.ExpectedVersion {
			return 0, conflict("version_conflict", "分组已变更，请重新加载")
		}
	}
	if kind == "category" {
		var exists int64
		if err = tx.QueryRow(ctx, "SELECT id FROM product_sections WHERE id=$1 FOR SHARE", *in.SectionID).Scan(&exists); err != nil {
			return 0, err
		}
	}
	action := "catalog_group.created"
	if id == 0 {
		if kind == "section" {
			err = tx.QueryRow(ctx, "INSERT INTO product_sections(name,slug,description,active,sort_order) VALUES($1,$2,$3,$4,$5) RETURNING id", in.Name, in.Slug, in.Description, *in.Active, in.SortOrder).Scan(&id)
		} else {
			err = tx.QueryRow(ctx, "INSERT INTO product_categories(name,slug,description,active,sort_order,section_id,headline,tagline) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id", in.Name, in.Slug, in.Description, *in.Active, in.SortOrder, in.SectionID, in.Headline, in.Tagline).Scan(&id)
		}
	} else {
		action = "catalog_group.updated"
		if kind == "section" {
			_, err = tx.Exec(ctx, "UPDATE product_sections SET name=$2,slug=$3,description=$4,active=$5,sort_order=$6,version=version+1,updated_at=now() WHERE id=$1", id, in.Name, in.Slug, in.Description, *in.Active, in.SortOrder)
		} else {
			_, err = tx.Exec(ctx, "UPDATE product_categories SET name=$2,slug=$3,description=$4,active=$5,sort_order=$6,section_id=$7,headline=$8,tagline=$9,version=version+1,updated_at=now() WHERE id=$1", id, in.Name, in.Slug, in.Description, *in.Active, in.SortOrder, in.SectionID, in.Headline, in.Tagline)
		}
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actor, action, kind, id, map[string]any{"version": version + 1, "active": *in.Active, "section_id": in.SectionID}); err != nil {
		return 0, err
	}
	receipt.ID = id
	if err = SaveCreationReceipt(ctx, tx, actor, "catalog_group.create."+kind, key, hash, receipt); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
func (s *Store) DeleteCatalogGroup(ctx context.Context, actor, id, version int64, kind string) error {
	table := CatalogGroupTable(kind)
	if table == "" {
		return conflict("invalid_catalog_group", "分组类型无效")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	if err = tx.QueryRow(ctx, "SELECT version FROM "+table+" WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		return err
	}
	if current != version {
		return conflict("version_conflict", "分组已变更，请重新加载")
	}
	var used bool
	query := "SELECT EXISTS(SELECT 1 FROM products WHERE category_id=$1)"
	if kind == "section" {
		query = "SELECT EXISTS(SELECT 1 FROM product_categories WHERE section_id=$1)"
	}
	if err = tx.QueryRow(ctx, query, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("catalog_group_in_use", "请先移走关联的分类或商品")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actor, "catalog_group.deleted", kind, id, map[string]any{"version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type ProductInput struct {
	Name                 string              `json:"name"`
	Slug                 string              `json:"slug"`
	Description          string              `json:"description"`
	CategoryID           *int64              `json:"category_id"`
	SortOrder            int                 `json:"sort_order"`
	Active               *bool               `json:"active"`
	BillingCycle         string              `json:"billing_cycle"`
	PriceCents           int64               `json:"price_cents"`
	SetupFeeCents        int64               `json:"setup_fee_cents"`
	Stock                *int                `json:"stock"`
	ExpectedStock        *int                `json:"expected_stock"`
	ProvisionMode        string              `json:"provision_mode"`
	DeliveryConfig       *provision.Delivery `json:"delivery_config"`
	RequireVerifiedPhone *bool               `json:"require_verified_phone"`
	RequireCertification *bool               `json:"require_certification"`
	ApprovalRequired     bool                `json:"approval_required"`
	ExpectedRevision     int64               `json:"expected_revision"`
}

var productSlugPattern = regexp.MustCompile(`^[a-z0-9-]{1,120}$`)

func (s *Store) SaveProduct(ctx context.Context, actor, id int64, in ProductInput) (int64, error) {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.ProvisionMode == "" {
		in.ProvisionMode = "manual"
	}
	if in.Active == nil && id == 0 {
		active := true
		in.Active = &active
	}
	if in.Name == "" || !contentTextValid(in.Name, 180, false) || !contentTextValid(in.Description, 10000, true) || !productSlugPattern.MatchString(in.Slug) || in.Active == nil || (in.CategoryID != nil && *in.CategoryID < 1) || in.SortOrder < 0 || in.SortOrder > 1000000 || in.PriceCents < 0 || in.PriceCents > 100000000 || in.SetupFeeCents < 0 || in.SetupFeeCents > 100000000 || (in.Stock != nil && (*in.Stock < 0 || *in.Stock > 2147483647)) || (in.ProvisionMode != "manual" && in.ProvisionMode != "simulation" && in.ProvisionMode != "nokvm" && in.ProvisionMode != "supplier") || (!supplier.IsBillingCycle(in.BillingCycle) || in.ProvisionMode != "supplier" && in.BillingCycle != "monthly" && in.BillingCycle != "quarterly" && in.BillingCycle != "annually" && in.BillingCycle != "once") {
		return 0, conflict("invalid_product", "商品名称、金额、账期、库存或可售状态无效")
	}
	deliveryJSON := []byte(`{}`)
	if in.ProvisionMode == "nokvm" {
		if in.DeliveryConfig == nil || in.DeliveryConfig.Validate() != nil || s.Provisioners[in.DeliveryConfig.ProfileID] == nil || len(s.ProvisionEncryptionKey) != 32 {
			return 0, conflict("invalid_delivery_config", "请选择已配置的 NOKVM 供应商并填写有效资源规格")
		}
		deliveryJSON, _ = json.Marshal(in.DeliveryConfig)
	} else if in.DeliveryConfig != nil {
		return 0, conflict("invalid_delivery_config", "该交付模式不接受供应商资源配置")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if in.ProvisionMode == "nokvm" && id != 0 {
		var configurable bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM product_option_groups WHERE product_id=$1 AND active)", id).Scan(&configurable); err != nil {
			return 0, err
		}
		if configurable {
			return 0, conflict("nokvm_fixed_resources", "当前 NOKVM 交付使用固定规格，请先停用可配置选项")
		}
	}
	if id != 0 {
		var oldMode string
		if err = tx.QueryRow(ctx, "SELECT provision_mode FROM products WHERE id=$1", id).Scan(&oldMode); err != nil {
			return 0, err
		}
		if oldMode == "supplier" && in.ProvisionMode != "supplier" {
			return 0, conflict("supplier_source_locked", "供应商来源不可更改，请新建或重新导入商品")
		}
	}
	if in.ProvisionMode == "supplier" {
		if id == 0 {
			return 0, conflict("supplier_import_required", "请从供应商目录导入商品")
		}
		if err = RequireSupplierProductCycle(ctx, tx, id, in.BillingCycle); err != nil {
			return 0, err
		}
		var defaultCycle string
		if err = tx.QueryRow(ctx, "SELECT billing_cycle FROM products WHERE id=$1", id).Scan(&defaultCycle); err != nil {
			return 0, err
		}
		if defaultCycle != in.BillingCycle {
			return 0, conflict("supplier_cycle_locked", "供应商商品的默认账期不可更改，请在价格页维护已导入的账期")
		}
		if err = tx.QueryRow(ctx, "SELECT delivery_config FROM products WHERE id=$1 AND provision_mode='supplier'", id).Scan(&deliveryJSON); err != nil {
			return 0, err
		}
	}
	var revision int64
	var stock *int
	if id != 0 {
		if err = tx.QueryRow(ctx, "SELECT revision,stock FROM products WHERE id=$1 FOR UPDATE", id).Scan(&revision, &stock); err != nil {
			return 0, err
		}
		if revision != in.ExpectedRevision {
			return 0, conflict("version_conflict", "商品已变更，请重新加载")
		}
		if (stock == nil) != (in.ExpectedStock == nil) || (stock != nil && *stock != *in.ExpectedStock) {
			return 0, conflict("stock_changed", "库存已变更，请重新加载后确认")
		}
	}
	if in.CategoryID != nil {
		var exists int64
		if err = tx.QueryRow(ctx, "SELECT id FROM product_categories WHERE id=$1 FOR SHARE", *in.CategoryID).Scan(&exists); err != nil {
			return 0, err
		}
	}
	action := "product.created"
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO products(name,slug,description,category_id,sort_order,active,billing_cycle,price_cents,setup_fee_cents,stock,provision_mode,approval_required,delivery_config) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`, in.Name, in.Slug, in.Description, in.CategoryID, in.SortOrder, *in.Active, in.BillingCycle, in.PriceCents, in.SetupFeeCents, in.Stock, in.ProvisionMode, in.ApprovalRequired, deliveryJSON).Scan(&id)
	} else {
		action = "product.updated"
		_, err = tx.Exec(ctx, `UPDATE products SET name=$2,slug=$3,description=$4,category_id=$5,sort_order=$6,active=$7, billing_cycle=$8,price_cents=$9,setup_fee_cents=$10,stock=$11,provision_mode=$12,approval_required=$13,delivery_config=$14,revision=revision+1,updated_at=now() WHERE id=$1`, id, in.Name, in.Slug, in.Description, in.CategoryID, in.SortOrder, *in.Active, in.BillingCycle, in.PriceCents, in.SetupFeeCents, in.Stock, in.ProvisionMode, in.ApprovalRequired, deliveryJSON)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO product_prices(product_id,billing_cycle,price_cents,setup_fee_cents,active) VALUES($1,$2,$3,$4,true) ON CONFLICT(product_id,billing_cycle) DO UPDATE SET price_cents=EXCLUDED.price_cents,setup_fee_cents=EXCLUDED.setup_fee_cents,active=true,updated_at=now()`, id, in.BillingCycle, in.PriceCents, in.SetupFeeCents)
		}
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "UPDATE products SET require_verified_phone=COALESCE($2,require_verified_phone),require_certification=COALESCE($3,require_certification) WHERE id=$1", id, in.RequireVerifiedPhone, in.RequireCertification); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actor, action, "product", id, map[string]any{"revision": revision + 1, "category_id": in.CategoryID, "active": *in.Active, "price_cents": in.PriceCents, "stock": in.Stock, "require_verified_phone": in.RequireVerifiedPhone, "require_certification": in.RequireCertification}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
