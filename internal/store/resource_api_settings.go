package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// ResourceAPISettings is the global policy shown on the legacy API setup
// page. It deliberately contains no secret; individual API keys are stored in
// api_keys and supplier credentials in supplier_accounts.
type ResourceAPISettings struct {
	Enabled              bool      `json:"enabled"`
	RequireCertification bool      `json:"require_certification"`
	RequireVerifiedPhone bool      `json:"require_verified_phone"`
	Version              int64     `json:"version"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type ResourceAPISettingsInput struct {
	Enabled              *bool `json:"enabled"`
	RequireCertification *bool `json:"require_certification"`
	RequireVerifiedPhone *bool `json:"require_verified_phone"`
	ExpectedVersion      int64 `json:"expected_version"`
}

func (s *Store) ResourceAPISettings(ctx context.Context) (ResourceAPISettings, error) {
	var out ResourceAPISettings
	err := s.Pool.QueryRow(ctx, `SELECT enabled,require_certification,require_verified_phone,version,updated_at
FROM resource_api_settings WHERE id=1`).Scan(&out.Enabled, &out.RequireCertification, &out.RequireVerifiedPhone, &out.Version, &out.UpdatedAt)
	return out, err
}

// CheckCustomerAPIKeyEligibilityTx gates the one-time issuance of a customer
// API secret. Admin API keys are intentionally outside this policy.
func (s *Store) CheckCustomerAPIKeyEligibilityTx(ctx context.Context, tx pgx.Tx, userID int64) error {
	var enabled, requireCertification, requirePhone bool
	if err := tx.QueryRow(ctx, `SELECT enabled,require_certification,require_verified_phone
FROM resource_api_settings WHERE id=1 FOR SHARE`).Scan(&enabled, &requireCertification, &requirePhone); err != nil {
		return err
	}
	if !enabled {
		return conflict("api_access_disabled", "客户 API 接口暂未开放")
	}
	var phoneVerified bool
	if err := tx.QueryRow(ctx, `SELECT phone_verified AND phone<>'' FROM users WHERE id=$1 FOR SHARE`, userID).Scan(&phoneVerified); err != nil {
		return err
	}
	if requirePhone && !phoneVerified {
		return conflict("api_phone_required", "获取 API 密钥前请先验证并绑定手机号")
	}
	if !requireCertification {
		return nil
	}
	var profileID int64
	var kind, status string
	if err := tx.QueryRow(ctx, `SELECT id,certification_type,status FROM certification_profiles WHERE user_id=$1 FOR SHARE`, userID).Scan(&profileID, &kind, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("api_certification_required", "获取 API 密钥前请完成并通过实名认证审核")
		}
		return err
	}
	var submissionStatus string
	var reviewedAt *time.Time
	var sealed []byte
	var aad string
	err := tx.QueryRow(ctx, `SELECT status,reviewed_at,payload_encrypted,payload_aad FROM certification_submissions
WHERE profile_id=$1 AND user_id=$2 ORDER BY id DESC LIMIT 1 FOR SHARE`, profileID, userID).Scan(&submissionStatus, &reviewedAt, &sealed, &aad)
	if errors.Is(err, pgx.ErrNoRows) {
		return conflict("api_certification_required", "获取 API 密钥前请完成并通过实名认证审核")
	}
	if err != nil {
		return err
	}
	if status != "approved" || submissionStatus != "approved" || reviewedAt == nil {
		return conflict("api_certification_required", "获取 API 密钥前请完成并通过实名认证审核")
	}
	payload, err := openCertification(s.ProvisionEncryptionKey, aad, sealed)
	if err != nil || validateCertificationPayload(kind, payload) != nil {
		return conflict("api_certification_required", "实名认证资料尚未通过有效性检查")
	}
	return nil
}

// resourceAPIManagerCheck rechecks both the browser session and the product
// management grant after the advisory lock has been acquired. This page lives
// under the supplier/product domain in the admin shell, so products.manage is
// the write permission instead of settings.manage.
func resourceAPIManagerCheck(ctx context.Context, tx pgx.Tx, actor int64, sessionHash string) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE`, actor).Scan(&role, &roleID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("permission_denied", "没有管理供应商 API 的权限")
		}
		return err
	}
	if role != "super_admin" {
		if role != "admin" || roleID == nil {
			return conflict("permission_denied", "没有管理供应商 API 的权限")
		}
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT active AND 'products.view'=ANY(permissions) AND 'products.manage'=ANY(permissions)
FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return conflict("permission_denied", "没有管理供应商 API 的权限")
		}
	}
	var sessionUser int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`, sessionHash, actor).Scan(&sessionUser); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("unauthorized", "登录已失效")
		}
		return err
	}
	return nil
}

func (s *Store) UpdateResourceAPISettings(ctx context.Context, actor int64, sessionHash, key string, in ResourceAPISettingsInput) (ResourceAPISettings, error) {
	var out ResourceAPISettings
	if in.Enabled == nil || in.RequireCertification == nil || in.RequireVerifiedPhone == nil || in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_resource_api_settings", "请提供完整的资源 API 设置、有效版本及幂等键")
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = resourceAPIManagerCheck(ctx, tx, actor, sessionHash); err != nil {
		return out, err
	}
	if replayed, err := ReplayOperation(ctx, tx, actor, "admin:resource_api.settings", key, hash, &out); err != nil {
		return out, err
	} else if replayed {
		return out, tx.Commit(ctx)
	}
	var current ResourceAPISettings
	if err = tx.QueryRow(ctx, `SELECT enabled,require_certification,require_verified_phone,version,updated_at
FROM resource_api_settings WHERE id=1 FOR UPDATE`).Scan(&current.Enabled, &current.RequireCertification, &current.RequireVerifiedPhone, &current.Version, &current.UpdatedAt); err != nil {
		return out, err
	}
	if current.Version != in.ExpectedVersion {
		return out, conflict("version_conflict", "资源 API 设置已变更，请重新加载")
	}
	if err = tx.QueryRow(ctx, `UPDATE resource_api_settings SET enabled=$1,require_certification=$2,
require_verified_phone=$3,version=version+1,updated_at=now() WHERE id=1
RETURNING enabled,require_certification,require_verified_phone,version,updated_at`, *in.Enabled, *in.RequireCertification, *in.RequireVerifiedPhone).
		Scan(&out.Enabled, &out.RequireCertification, &out.RequireVerifiedPhone, &out.Version, &out.UpdatedAt); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "resource_api.settings_updated", "resource_api_settings", 1, map[string]any{
		"enabled": out.Enabled, "require_certification": out.RequireCertification,
		"require_verified_phone": out.RequireVerifiedPhone, "version": out.Version,
	}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:resource_api.settings", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

type SupplierAccountSummary struct {
	ID                    int64  `json:"id"`
	Name                  string `json:"name"`
	Provider              string `json:"provider"`
	BaseURL               string `json:"base_url"`
	Username              string `json:"username"`
	Active                bool   `json:"active"`
	Version               int64  `json:"version"`
	CredentialsConfigured bool   `json:"credentials_configured"`
	ProductCount          int64  `json:"product_count"`
	RunningJobID          *int64 `json:"running_job_id,omitempty"`
}

func (s *Store) SupplierAccountSummaries(ctx context.Context) ([]SupplierAccountSummary, error) {
	rows, err := s.Pool.Query(ctx, `SELECT a.id,a.name,a.provider,a.base_url,a.username,a.active,a.version,
octet_length(a.credentials_encrypted)>0,(SELECT count(*) FROM supplier_product_bindings b WHERE b.supplier_id=a.id),a.running_job_id
FROM supplier_accounts a ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SupplierAccountSummary{}
	for rows.Next() {
		var item SupplierAccountSummary
		if err = rows.Scan(&item.ID, &item.Name, &item.Provider, &item.BaseURL, &item.Username, &item.Active, &item.Version, &item.CredentialsConfigured, &item.ProductCount, &item.RunningJobID); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type SupplierConnectionResult struct {
	SupplierID   int64                 `json:"supplier_id"`
	Provider     string                `json:"provider"`
	ProductCount int                   `json:"product_count"`
	Capabilities supplier.Capabilities `json:"capabilities"`
	CheckedAt    time.Time             `json:"checked_at"`
	DurationMS   int64                 `json:"duration_ms"`
	OK           bool                  `json:"ok"`
	Message      string                `json:"message"`
	Replayed     bool                  `json:"replayed,omitempty"`
}

// TestSupplierConnection performs a read-only upstream catalogue request. No
// database transaction remains open while the remote supplier is contacted.
func (s *Store) TestSupplierConnection(ctx context.Context, actor int64, sessionHash string, supplierID, expectedVersion int64, key string) (SupplierConnectionResult, error) {
	if supplierID < 1 || expectedVersion < 1 || len(key) < 8 || len(key) > 128 {
		return SupplierConnectionResult{}, conflict("invalid_supplier_test", "供应商编号、版本或幂等键无效")
	}
	requestHashBytes := sha256.Sum256([]byte(fmt.Sprintf("supplier.connection_test:%d:%d", supplierID, expectedVersion)))
	requestHash := hex.EncodeToString(requestHashBytes[:])
	// Verify the write-capable admin grant before reading encrypted credentials
	// or contacting the upstream. The reservation transaction rechecks it again;
	// finalization only records the already-authorized read-only probe and
	// invalidates its result if the supplier configuration changed meanwhile.
	authorize, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return SupplierConnectionResult{}, err
	}
	if err = resourceAPIManagerCheck(ctx, authorize, actor, sessionHash); err != nil {
		_ = authorize.Rollback(ctx)
		return SupplierConnectionResult{}, err
	}
	if err = authorize.Commit(ctx); err != nil {
		return SupplierConnectionResult{}, err
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return SupplierConnectionResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = resourceAPIManagerCheck(ctx, tx, actor, sessionHash); err != nil {
		return SupplierConnectionResult{}, err
	}
	var result SupplierConnectionResult
	if replayed, err := ReplayOperation(ctx, tx, actor, "admin:supplier.connection_test", key, requestHash, &result); err != nil {
		return SupplierConnectionResult{}, err
	} else if replayed {
		if result.Message == "测试进行中" {
			return SupplierConnectionResult{}, conflict("supplier_test_in_progress", result.Message)
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	var account supplier.Account
	var sealed []byte
	var active bool
	var version int64
	if err = tx.QueryRow(ctx, `SELECT id,provider,base_url,username,credentials_encrypted,active,version
FROM supplier_accounts WHERE id=$1 FOR SHARE`, supplierID).Scan(&account.ID, &account.Provider, &account.BaseURL, &account.Username, &sealed, &active, &version); err != nil {
		return SupplierConnectionResult{}, err
	}
	if version != expectedVersion {
		return SupplierConnectionResult{}, conflict("version_conflict", "供应商配置已变更，请重新加载")
	}
	if !active {
		return SupplierConnectionResult{}, conflict("supplier_disabled", "供应商已停用")
	}
	if err = supplierUnseal(s.ProvisionEncryptionKey, "account", supplierID, sealed, &account.Password); err != nil {
		return SupplierConnectionResult{}, err
	}
	adapter, adapterErr := supplier.New(account)
	if adapterErr != nil {
		return SupplierConnectionResult{}, conflict("invalid_supplier", "供应商配置无效，请检查接口配置")
	}
	pending := SupplierConnectionResult{SupplierID: supplierID, Provider: account.Provider, Message: "测试进行中"}
	if err = SaveOperation(ctx, tx, actor, "admin:supplier.connection_test", key, requestHash, pending); err != nil {
		return SupplierConnectionResult{}, err
	}
	if err = Audit(ctx, tx, actor, "supplier.connection_test_started", "supplier", supplierID, map[string]any{"version": version}); err != nil {
		return SupplierConnectionResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SupplierConnectionResult{}, err
	}

	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	products, probeErr := adapter.Catalog(probeCtx)
	cancel()
	result = SupplierConnectionResult{SupplierID: supplierID, Provider: account.Provider, ProductCount: len(products), Capabilities: adapter.Capabilities(), CheckedAt: time.Now().UTC(), DurationMS: time.Since(started).Milliseconds()}
	if probeErr == nil {
		result.OK, result.Message = true, "连接成功"
	} else {
		result.Message = "连接失败，请检查接口地址、账户凭据或上游状态"
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return SupplierConnectionResult{}, err
	}
	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finalCancel()
	finish, err := s.CatalogTx(finalCtx, actor)
	if err != nil {
		return SupplierConnectionResult{}, err
	}
	defer finish.Rollback(finalCtx)
	var currentVersion int64
	if err = finish.QueryRow(finalCtx, "SELECT version FROM supplier_accounts WHERE id=$1 FOR SHARE", supplierID).Scan(&currentVersion); err != nil {
		return SupplierConnectionResult{}, err
	}
	if currentVersion != version {
		result.OK = false
		result.Message = "配置在测试期间发生变化，本次结果已作废，请重新测试"
		resultJSON, _ = json.Marshal(result)
	}
	if err = Audit(finalCtx, finish, actor, "supplier.connection_test_completed", "supplier", supplierID, map[string]any{"ok": result.OK, "duration_ms": result.DurationMS, "product_count": result.ProductCount, "version": version}); err != nil {
		return SupplierConnectionResult{}, err
	}
	if _, err = finish.Exec(finalCtx, `UPDATE mutation_operations SET result=$4
WHERE actor_id=$1 AND scope='admin:supplier.connection_test' AND idempotency_key=$2 AND request_hash=$3`, actor, key, requestHash, resultJSON); err != nil {
		return SupplierConnectionResult{}, err
	}
	if err = finish.Commit(finalCtx); err != nil {
		return SupplierConnectionResult{}, err
	}
	return result, nil
}
