package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

type SupplierInput struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Active          *bool  `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

type SupplierImportInput struct {
	UpstreamProductID        int64            `json:"upstream_product_id"`
	ExpectedSupplierVersion  int64            `json:"expected_supplier_version"`
	ExpectedUpstreamRevision string           `json:"expected_upstream_revision"`
	Name                     string           `json:"name"`
	Slug                     string           `json:"slug"`
	CategoryID               *int64           `json:"category_id"`
	BillingCycle             string           `json:"billing_cycle"`
	PriceCents               int64            `json:"price_cents"`
	SetupFeeCents            int64            `json:"setup_fee_cents"`
	Stock                    *int             `json:"stock"`
	Active                   bool             `json:"active"`
	ConfigOptions            map[int64]string `json:"config_options"`
	CustomFields             map[int64]string `json:"custom_fields"`
	MaximumCostCents         int64            `json:"maximum_cost_cents"`
}

type SupplierBinding struct {
	ID                     int64            `json:"id"`
	ProductID              int64            `json:"product_id"`
	SupplierID             int64            `json:"supplier_id"`
	Provider               string           `json:"provider"`
	UpstreamProductID      int64            `json:"upstream_product_id"`
	UpstreamRevision       string           `json:"upstream_revision"`
	BillingCycle           string           `json:"billing_cycle"`
	Currency               string           `json:"currency"`
	MaximumCostCents       int64            `json:"maximum_cost_cents"`
	ConfigOptions          map[int64]string `json:"config_options"`
	CustomFieldsConfigured bool             `json:"custom_fields_configured"`
	sealed                 []byte
}

type supplierSnapshot struct {
	SupplierID int64  `json:"supplier_id"`
	BindingID  int64  `json:"binding_id"`
	Provider   string `json:"provider"`
}

func (s *Store) SaveSupplier(ctx context.Context, actor, id int64, in SupplierInput) (int64, error) {
	in.Name, in.BaseURL, in.Username = strings.TrimSpace(in.Name), strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"), strings.TrimSpace(in.Username)
	if !contentTextValid(in.Name, 180, false) || in.Name == "" || len(in.Username) > 255 || in.Username == "" || len(in.Password) > 4096 || in.Active == nil || len(s.ProvisionEncryptionKey) != 32 {
		return 0, conflict("invalid_supplier", "名称、账户、状态或加密配置无效")
	}
	// The registry rejects unsupported providers and malformed URLs before persistence.
	if _, err := supplier.New(supplier.Account{Provider: in.Provider, BaseURL: in.BaseURL, Username: in.Username, Password: "validation"}); err != nil {
		return 0, conflict("invalid_supplier", "供应商类型或接口地址无效")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if id == 0 {
		if in.Password == "" {
			return 0, conflict("supplier_password_required", "请填写供应商账户密码")
		}
		err = tx.QueryRow(ctx, `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,active) VALUES($1,$2,$3,$4,''::bytea,$5) RETURNING id`, in.Name, in.Provider, in.BaseURL, in.Username, *in.Active).Scan(&id)
		if err != nil {
			return 0, err
		}
	} else {
		var version int64
		var provider, base, username string
		var running *int64
		err = tx.QueryRow(ctx, `SELECT version,provider,base_url,username,running_job_id FROM supplier_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&version, &provider, &base, &username, &running)
		if err != nil {
			return 0, err
		}
		if version != in.ExpectedVersion {
			return 0, conflict("version_conflict", "供应商已变更，请重新加载")
		}
		if provider != in.Provider || base != in.BaseURL || username != in.Username {
			return 0, conflict("supplier_identity_locked", "供应商来源及账户身份不可更改，请新建供应商")
		}
		if running != nil {
			return 0, conflict("supplier_busy", "供应商存在进行中或待核验的交付任务，请先处理")
		}
	}
	if in.Password != "" {
		sealed, e := supplierSeal(s.ProvisionEncryptionKey, "account", id, in.Password)
		if e != nil {
			return 0, e
		}
		if _, err = tx.Exec(ctx, `UPDATE supplier_accounts SET credentials_encrypted=$2 WHERE id=$1`, id, sealed); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE supplier_accounts SET name=$2,active=$3,version=CASE WHEN $4::bigint=0 THEN version ELSE version+1 END,updated_at=now() WHERE id=$1`, id, in.Name, *in.Active, in.ExpectedVersion); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actor, "supplier.saved", "supplier", id, map[string]any{"provider": in.Provider, "active": *in.Active, "password_changed": in.Password != ""}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) supplierAdapter(ctx context.Context, id int64) (supplier.Adapter, int64, error) {
	var a supplier.Account
	var sealed []byte
	var version int64
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT id,provider,base_url,username,credentials_encrypted,version,active FROM supplier_accounts WHERE id=$1`, id).Scan(&a.ID, &a.Provider, &a.BaseURL, &a.Username, &sealed, &version, &active)
	if err != nil {
		return nil, 0, err
	}
	if !active {
		return nil, 0, conflict("supplier_disabled", "供应商已停用")
	}
	if err = supplierUnseal(s.ProvisionEncryptionKey, "account", id, sealed, &a.Password); err != nil {
		return nil, 0, err
	}
	adapter, err := supplier.New(a)
	return adapter, version, err
}
func supplierReadError(err error) error {
	var e *supplier.Error
	if errors.As(err, &e) {
		return conflict("supplier_"+e.Code, "上游接口请求失败，请检查供应商连接或商品状态")
	}
	return err
}
func (s *Store) SupplierCatalog(ctx context.Context, id int64) ([]supplier.Product, error) {
	a, _, err := s.supplierAdapter(ctx, id)
	if err != nil {
		return nil, err
	}
	p, err := a.Catalog(ctx)
	return p, supplierReadError(err)
}
func (s *Store) SupplierProduct(ctx context.Context, id, pid int64) (supplier.Product, error) {
	a, _, err := s.supplierAdapter(ctx, id)
	if err != nil {
		return supplier.Product{}, err
	}
	p, err := a.Product(ctx, pid)
	return p, supplierReadError(err)
}

func (s *Store) ImportSupplierProduct(ctx context.Context, actor, supplierID int64, in SupplierImportInput) (int64, error) {
	in.Name, in.Slug = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Slug))
	if in.UpstreamProductID < 1 || in.ExpectedSupplierVersion < 1 || in.ExpectedUpstreamRevision == "" || in.Name == "" || !contentTextValid(in.Name, 180, false) || !productSlugPattern.MatchString(in.Slug) || in.PriceCents < 0 || in.PriceCents > 100000000 || in.SetupFeeCents < 0 || in.SetupFeeCents > 100000000 || in.MaximumCostCents < 0 || in.MaximumCostCents > 100000000 || (in.Stock != nil && (*in.Stock < 0 || *in.Stock > 2147483647)) || in.Active {
		return 0, conflict("invalid_supplier_import", "商品信息无效，请先导入为下架商品后确认上架")
	}
	// Authorize before any network request; reauthorize under transaction after it.
	authTx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	if err = authTx.Commit(ctx); err != nil {
		return 0, err
	}
	a, version, err := s.supplierAdapter(ctx, supplierID)
	if err != nil {
		return 0, err
	}
	if version != in.ExpectedSupplierVersion {
		return 0, conflict("version_conflict", "供应商已变更")
	}
	p, err := a.Product(ctx, in.UpstreamProductID)
	if err != nil {
		return 0, supplierReadError(err)
	}
	if !p.Supported || p.Currency != "CNY" || p.Revision != in.ExpectedUpstreamRevision {
		return 0, conflict("supplier_product_changed", "上游商品已变更或规格暂不支持，请重新预览")
	}
	cost, err := supplier.ValidateSelection(p, in.BillingCycle, in.ConfigOptions, in.CustomFields)
	if err != nil {
		return 0, supplierReadError(err)
	}
	if cost > in.MaximumCostCents {
		return 0, conflict("supplier_cost_limit", "成本上限低于所选上游规格价格")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var current int64
	var active bool
	var provider string
	if err = tx.QueryRow(ctx, `SELECT version,active,provider FROM supplier_accounts WHERE id=$1 FOR SHARE`, supplierID).Scan(&current, &active, &provider); err != nil {
		return 0, err
	}
	if !active || current != version {
		return 0, conflict("version_conflict", "供应商配置已变更，请重新预览")
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO products(name,slug,category_id,billing_cycle,price_cents,setup_fee_cents,stock,active,provision_mode) VALUES($1,$2,$3,$4,$5,$6,$7,false,'manual') RETURNING id`, in.Name, in.Slug, in.CategoryID, in.BillingCycle, in.PriceCents, in.SetupFeeCents, in.Stock).Scan(&id); err != nil {
		return 0, err
	}
	fields := in.CustomFields
	if fields == nil {
		fields = map[int64]string{}
	}
	sealed, err := supplierSeal(s.ProvisionEncryptionKey, "binding-fields", id, fields)
	if err != nil {
		return 0, err
	}
	options := in.ConfigOptions
	if options == nil {
		options = map[int64]string{}
	}
	raw, _ := json.Marshal(options)
	var bindingID int64
	if err = tx.QueryRow(ctx, `INSERT INTO supplier_product_bindings(product_id,supplier_id,provider,upstream_product_id,upstream_revision,billing_cycle,currency,maximum_cost_cents,config_options,custom_fields_encrypted) VALUES($1,$2,$3,$4,$5,$6,'CNY',$7,$8,$9) RETURNING id`, id, supplierID, provider, in.UpstreamProductID, p.Revision, in.BillingCycle, in.MaximumCostCents, raw, sealed).Scan(&bindingID); err != nil {
		return 0, err
	}
	snapshot, _ := json.Marshal(supplierSnapshot{SupplierID: supplierID, BindingID: bindingID, Provider: provider})
	if _, err = tx.Exec(ctx, `UPDATE products SET provision_mode='supplier',delivery_config=$2 WHERE id=$1`, id, snapshot); err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actor, "supplier.product.imported", "product", id, map[string]any{"supplier_id": supplierID, "binding_id": bindingID, "maximum_cost_cents": in.MaximumCostCents}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) supplierBinding(ctx context.Context, id int64, byProduct bool) (SupplierBinding, error) {
	var b SupplierBinding
	var raw []byte
	where := "id"
	if byProduct {
		where = "product_id"
	}
	err := s.Pool.QueryRow(ctx, `SELECT id,product_id,supplier_id,provider,upstream_product_id,upstream_revision,billing_cycle,currency,maximum_cost_cents,config_options,custom_fields_encrypted FROM supplier_product_bindings WHERE `+where+`=$1`, id).Scan(&b.ID, &b.ProductID, &b.SupplierID, &b.Provider, &b.UpstreamProductID, &b.UpstreamRevision, &b.BillingCycle, &b.Currency, &b.MaximumCostCents, &raw, &b.sealed)
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal(raw, &b.ConfigOptions); err != nil {
		return b, err
	}
	var fields map[int64]string
	if err = supplierUnseal(s.ProvisionEncryptionKey, "binding-fields", b.ProductID, b.sealed, &fields); err != nil {
		return b, err
	}
	b.CustomFieldsConfigured = len(fields) > 0
	return b, nil
}
func (s *Store) SupplierBinding(ctx context.Context, productID int64) (SupplierBinding, error) {
	return s.supplierBinding(ctx, productID, true)
}

// Bound provider, upstream identity and configuration must not be retargeted through ordinary product writes.
func RequireSupplierProductCycle(ctx context.Context, tx sqlQueryer, pid int64, cycle string) error {
	var mode string
	if err := tx.QueryRow(ctx, "SELECT provision_mode FROM products WHERE id=$1", pid).Scan(&mode); err != nil {
		return err
	}
	if mode != "supplier" {
		return nil
	}
	var bound string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT b.billing_cycle,a.active FROM supplier_product_bindings b JOIN supplier_accounts a ON a.id=b.supplier_id WHERE b.product_id=$1`, pid).Scan(&bound, &active); err != nil {
		return err
	}
	if !active {
		return conflict("supplier_disabled", "该商品供应商已停用")
	}
	if bound != cycle {
		return conflict("supplier_cycle_locked", "上游商品只接受导入时绑定的账期")
	}
	return nil
}

func lockSupplierAtCheckout(ctx context.Context, tx pgx.Tx, productID int64, cycle string) error {
	var bound string
	var active bool
	err := tx.QueryRow(ctx, `SELECT b.billing_cycle,a.active FROM supplier_product_bindings b JOIN supplier_accounts a ON a.id=b.supplier_id WHERE b.product_id=$1 FOR SHARE OF a`, productID).Scan(&bound, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !active || bound != cycle {
		return conflict("supplier_unavailable", "供应商已停用或上游商品账期不匹配")
	}
	return nil
}
