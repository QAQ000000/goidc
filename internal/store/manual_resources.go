package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type ManualResourceInput struct {
	Name              string `json:"name"`
	ResourceType      string `json:"resource_type"`
	Provider          string `json:"provider"`
	Region            string `json:"region"`
	Address           string `json:"address"`
	UpstreamID        string `json:"upstream_id"`
	CostCents         int64  `json:"cost_cents"`
	UpstreamExpiresAt string `json:"upstream_expires_at"`
	Status            string `json:"status"`
	Notes             string `json:"notes"`
	Username          string `json:"username"`
	Password          string `json:"password"`
}
type ManualResourcePatchInput struct {
	Name              *string `json:"name"`
	ResourceType      *string `json:"resource_type"`
	Provider          *string `json:"provider"`
	Region            *string `json:"region"`
	Address           *string `json:"address"`
	UpstreamID        *string `json:"upstream_id"`
	CostCents         *int64  `json:"cost_cents"`
	UpstreamExpiresAt *string `json:"upstream_expires_at"`
	Status            *string `json:"status"`
	Notes             *string `json:"notes"`
	Username          *string `json:"username"`
	Password          *string `json:"password"`
	ExpectedVersion   int64   `json:"expected_version"`
}
type ManualResourceReleaseInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note"`
}
type ManualResourceFilter struct {
	Q, Status, Binding string
	Limit, Offset      int
}

type manualResourceSecret struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ManualResourceDelivery is an internal transaction value, never an HTTP or
// receipt payload. The caller re-seals Credentials for the destination service.
type ManualResourceDelivery struct {
	UpstreamID, Username, Address string
	Credentials                   provision.Credentials
	ResourceVersion               int64
}

func manualResourceError(status int, code, message string) error {
	return &AccountBusinessError{status, code, message}
}
func manualResourceKey(key string) (string, error) {
	if key != strings.TrimSpace(key) || len(key) < 8 || len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return "", manualResourceError(400, "invalid_idempotency_key", "请提供长度为8至128的幂等键")
	}
	return key, nil
}
func manualResourceExpiry(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Year() < 2000 || parsed.Year() > 2200 {
		return nil, manualResourceError(400, "invalid_manual_resource", "上游到期时间须为有效RFC3339时间")
	}
	return &parsed, nil
}
func normalizeManualResource(in *ManualResourceInput) error {
	for _, field := range []*string{&in.Name, &in.ResourceType, &in.Provider, &in.Region, &in.Address, &in.UpstreamID, &in.Status, &in.Notes, &in.Username} {
		*field = strings.TrimSpace(*field)
	}
	if in.ResourceType == "" {
		in.ResourceType = "server"
	}
	if in.Status == "" {
		in.Status = "available"
	}
	if !utf8.ValidString(in.Name) || !utf8.ValidString(in.ResourceType) || !utf8.ValidString(in.Provider) || !utf8.ValidString(in.Region) || !utf8.ValidString(in.Address) || !utf8.ValidString(in.UpstreamID) || !utf8.ValidString(in.Notes) || strings.IndexFunc(in.Address, unicode.IsSpace) >= 0 || in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || utf8.RuneCountInString(in.ResourceType) > 40 ||
		utf8.RuneCountInString(in.Provider) > 120 || utf8.RuneCountInString(in.Region) > 120 ||
		utf8.RuneCountInString(in.Address) > 256 || utf8.RuneCountInString(in.UpstreamID) > 256 ||
		utf8.RuneCountInString(in.Notes) > 4000 || len(in.Username) > 256 || len(in.Password) > 4096 ||
		in.CostCents < 0 || in.CostCents > 9000000000000000 ||
		(in.Status != "available" && in.Status != "disabled" && in.Status != "retired") {
		return manualResourceError(400, "invalid_manual_resource", "人工资源字段或状态无效")
	}
	_, err := manualResourceExpiry(in.UpstreamExpiresAt)
	return err
}

// LockManualResourceMutation follows RBAC -> account/role -> credential before
// any receipt, service, job or inventory row. The exclusive RBAC writer uses the
// same advisory lock. Recheck after business waits and immediately before commit.
func LockManualResourceMutation(ctx context.Context, tx pgx.Tx, actor int64) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	if err := lockAdminCreationPrincipal(ctx, tx, actor, "resources"); err != nil {
		if err == pgx.ErrNoRows {
			return manualResourceError(403, "permission_denied", "没有管理人工资源的权限")
		}
		var denied *Conflict
		if errors.As(err, &denied) && denied.Code == "permission_denied" {
			return manualResourceError(403, denied.Code, denied.Message)
		}
		return err
	}
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return manualResourceError(401, "unauthorized", "需要有效管理员凭据")
	}
	return RecheckManualResourceMutation(ctx, tx, actor)
}
func RecheckManualResourceMutation(ctx context.Context, tx pgx.Tx, actor int64) error {
	check := func() error {
		err := lockStorageCredential(ctx, tx, actor)
		var denied *Conflict
		if errors.As(err, &denied) && denied.Code == "unauthorized" {
			return manualResourceError(401, denied.Code, denied.Message)
		}
		return err
	}
	if err := check(); err != nil {
		return err
	}
	return check()
}
func manualResourceCommit(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := RecheckManualResourceMutation(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func manualResourceReadAuthority(ctx context.Context, tx pgx.Tx, actor int64) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND 'resources.view'=ANY(r.permissions))))`, actor).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return manualResourceError(403, "permission_denied", "没有读取人工资源的权限")
	}
	return nil
}

const manualResourceVisible = `((b.id IS NOT NULL AND admin_customer_allowed($1,s.user_id)) OR
 (b.id IS NULL AND EXISTS(SELECT 1 FROM administrators a WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR a.customer_scope='all'))))`
const manualResourcePublicSelect = `SELECT r.id,r.name,r.resource_type,r.provider,r.region,r.address,r.upstream_id,r.cost_cents,r.currency,
 r.upstream_expires_at,r.status,r.notes,r.version,r.created_at,r.updated_at,(octet_length(r.credentials_encrypted)>0) AS has_credentials,
 b.id AS binding_id,b.service_id,s.user_id AS customer_id,b.created_at AS bound_at
 FROM manual_resources r LEFT JOIN manual_resource_bindings b ON b.resource_id=r.id AND b.released_at IS NULL
 LEFT JOIN services s ON s.id=b.service_id`

func (s *Store) ListManualResources(ctx context.Context, actor int64, f ManualResourceFilter) (map[string]any, error) {
	if utf8.RuneCountInString(f.Q) > 120 || (f.Status != "" && f.Status != "available" && f.Status != "disabled" && f.Status != "retired") || (f.Binding != "" && f.Binding != "bound" && f.Binding != "unbound") || f.Limit < 1 || f.Limit > 100 || f.Offset < 0 {
		return nil, manualResourceError(400, "invalid_manual_resource_filter", "人工资源筛选无效")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = manualResourceReadAuthority(ctx, tx, actor); err != nil {
		return nil, err
	}
	query := manualResourcePublicSelect + ` WHERE ` + manualResourceVisible + ` AND ($2='' OR r.name ILIKE '%'||$2||'%' OR r.address ILIKE '%'||$2||'%' OR r.upstream_id ILIKE '%'||$2||'%' OR r.provider ILIKE '%'||$2||'%')
 AND ($3='' OR r.status=$3) AND ($4='' OR ($4='bound' AND b.id IS NOT NULL) OR ($4='unbound' AND b.id IS NULL))`
	page, err := customerAccountReadPage(ctx, tx, query, "id DESC", []any{actor, f.Q, f.Status, f.Binding}, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	return page, tx.Commit(ctx)
}
func (s *Store) ManualResource(ctx context.Context, actor, id int64) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = manualResourceReadAuthority(ctx, tx, actor); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, manualResourcePublicSelect+` WHERE r.id=$2 AND `+manualResourceVisible, actor, id)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err == pgx.ErrNoRows {
		return nil, manualResourceError(404, "not_found", "人工资源不存在")
	}
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) manualResourceFingerprint(payload any, secret bool) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if secret {
		if len(s.ProvisionEncryptionKey) != 32 {
			return "", manualResourceError(503, "manual_resource_encryption_unconfigured", "人工资源凭据加密密钥未配置")
		}
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		mac.Write([]byte("manual-resource-receipt:"))
		mac.Write(raw)
		hash = hex.EncodeToString(mac.Sum(nil))
	}
	return hash, nil
}
func (s *Store) manualResourceReceipt(ctx context.Context, tx pgx.Tx, actor int64, operation, key string, payload any, secret bool) (string, map[string]any, bool, error) {
	hash, err := s.manualResourceFingerprint(payload, secret)
	if err != nil {
		return "", nil, false, err
	}
	out := map[string]any{}
	replay, err := ReplayOperation(ctx, tx, actor, "admin:"+operation, key, hash, &out)
	if replay {
		out["replayed"] = true
	}
	return hash, out, replay, err
}
func manualResourceSeal(key []byte, id int64, value manualResourceSecret) ([]byte, error) {
	if value.Username == "" && value.Password == "" {
		return []byte{}, nil
	}
	if len(key) != 32 {
		return nil, manualResourceError(503, "manual_resource_encryption_unconfigured", "人工资源凭据加密密钥未配置")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return secure.Seal(key, []byte(fmt.Sprintf("manual-resource:%d", id)), raw)
}
func manualResourceUnseal(key []byte, id int64, sealed []byte) (manualResourceSecret, error) {
	var value manualResourceSecret
	if len(sealed) == 0 {
		return value, nil
	}
	if len(key) != 32 {
		return value, manualResourceError(503, "manual_resource_encryption_unconfigured", "人工资源凭据加密密钥未配置")
	}
	raw, err := secure.Open(key, []byte(fmt.Sprintf("manual-resource:%d", id)), sealed)
	if err != nil || json.Unmarshal(raw, &value) != nil {
		return value, manualResourceError(409, "manual_resource_credentials_invalid", "人工资源凭据不可用")
	}
	return value, nil
}
func (s *Store) CreateManualResource(ctx context.Context, actor int64, key string, in ManualResourceInput) (map[string]any, bool, error) {
	key, err := manualResourceKey(key)
	if err != nil {
		return nil, false, err
	}
	if err = normalizeManualResource(&in); err != nil {
		return nil, false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	if err = LockManualResourceMutation(ctx, tx, actor); err != nil {
		return nil, false, err
	}
	var full bool
	if err = tx.QueryRow(ctx, "SELECT role='super_admin' OR customer_scope='all' FROM administrators WHERE id=$1", actor).Scan(&full); err != nil {
		return nil, false, err
	}
	if !full {
		return nil, false, manualResourceError(403, "permission_denied", "未分配资源仅允许全客户范围管理员管理")
	}
	const operation = "manual_resource.create"
	hash, out, replay, err := s.manualResourceReceipt(ctx, tx, actor, operation, key, in, in.Username != "" || in.Password != "")
	if err != nil {
		return nil, false, err
	}
	if replay {
		return out, true, manualResourceCommit(ctx, tx, actor)
	}
	expiry, _ := manualResourceExpiry(in.UpstreamExpiresAt)
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO manual_resources(name,resource_type,provider,region,address,upstream_id,cost_cents,upstream_expires_at,status,notes)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, in.Name, in.ResourceType, in.Provider, in.Region, in.Address, in.UpstreamID, in.CostCents, expiry, in.Status, in.Notes).Scan(&id); err != nil {
		return nil, false, err
	}
	sealed, err := manualResourceSeal(s.ProvisionEncryptionKey, id, manualResourceSecret{in.Username, in.Password})
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE manual_resources SET credentials_encrypted=$2 WHERE id=$1", id, sealed); err != nil {
		return nil, false, err
	}
	out = map[string]any{"id": id, "version": int64(1), "replayed": false}
	if err = Audit(ctx, tx, actor, "manual_resource.created", "manual_resource", id, map[string]any{"status": in.Status, "cost_cents": in.CostCents, "has_credentials": len(sealed) > 0}); err != nil {
		return nil, false, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, out); err != nil {
		return nil, false, err
	}
	return out, false, manualResourceCommit(ctx, tx, actor)
}

// Resolve scope before the inventory lock, then compare the active customer
// after waiting. Never acquire a different customer scope behind an inventory
// row: a concurrent release/rebind must make this request restart instead.
func manualResourceScope(ctx context.Context, tx pgx.Tx, actor, id int64) (int64, error) {
	var customer *int64
	var full bool
	err := tx.QueryRow(ctx, `SELECT s.user_id,(a.role='super_admin' OR a.customer_scope='all') FROM manual_resources r
 CROSS JOIN administrators a LEFT JOIN manual_resource_bindings b ON b.resource_id=r.id AND b.released_at IS NULL
 LEFT JOIN services s ON s.id=b.service_id WHERE r.id=$2 AND a.id=$1`, actor, id).Scan(&customer, &full)
	if err == pgx.ErrNoRows {
		return 0, manualResourceError(404, "not_found", "人工资源不存在")
	}
	if err != nil {
		return 0, err
	}
	if customer == nil {
		if !full {
			return 0, manualResourceError(404, "not_found", "人工资源不存在")
		}
		return 0, nil
	}
	if err = LockAdminCustomer(ctx, tx, actor, *customer); err != nil {
		if err == pgx.ErrNoRows {
			return 0, manualResourceError(404, "not_found", "人工资源不存在")
		}
		return 0, err
	}
	return *customer, nil
}
func manualResourceScopeAfterWait(ctx context.Context, tx pgx.Tx, actor, id, customer int64) error {
	var current int64
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT coalesce(s.user_id,0),`+manualResourceVisible+` FROM manual_resources r
 LEFT JOIN manual_resource_bindings b ON b.resource_id=r.id AND b.released_at IS NULL
 LEFT JOIN services s ON s.id=b.service_id WHERE r.id=$2`, actor, id).Scan(&current, &allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return manualResourceError(404, "not_found", "人工资源不存在")
	}
	if current != customer {
		return manualResourceError(409, "manual_resource_binding_changed", "人工资源绑定已变更，请刷新后重试")
	}
	return RecheckManualResourceMutation(ctx, tx, actor)
}
func (s *Store) UpdateManualResource(ctx context.Context, actor, id int64, key string, in ManualResourcePatchInput) (map[string]any, error) {
	key, err := manualResourceKey(key)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion < 1 {
		return nil, manualResourceError(409, "manual_resource_version_required", "请提交人工资源当前版本")
	}
	if in.Name == nil && in.ResourceType == nil && in.Provider == nil && in.Region == nil && in.Address == nil && in.UpstreamID == nil && in.CostCents == nil && in.UpstreamExpiresAt == nil && in.Status == nil && in.Notes == nil && in.Username == nil && in.Password == nil {
		return nil, manualResourceError(400, "invalid_manual_resource", "至少提供一个修改字段")
	}
	// Defaults belong to creation. Clearing a required enum/type in a PATCH
	// must not silently reactivate disabled inventory or change its type.
	if (in.Status != nil && strings.TrimSpace(*in.Status) == "") ||
		(in.ResourceType != nil && strings.TrimSpace(*in.ResourceType) == "") {
		return nil, manualResourceError(400, "invalid_manual_resource", "库存状态和资源类型不能清空")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = LockManualResourceMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	customer, err := manualResourceScope(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("manual_resource.update:%d", id)
	hash, out, replay, err := s.manualResourceReceipt(ctx, tx, actor, operation, key, in, in.Username != nil || in.Password != nil)
	if err != nil {
		return nil, err
	}
	var current ManualResourceInput
	var expiry *time.Time
	var version int64
	var sealed []byte
	if err = tx.QueryRow(ctx, `SELECT name,resource_type,provider,region,address,upstream_id,cost_cents,upstream_expires_at,status,notes,version,credentials_encrypted FROM manual_resources WHERE id=$1 FOR UPDATE`, id).Scan(&current.Name, &current.ResourceType, &current.Provider, &current.Region, &current.Address, &current.UpstreamID, &current.CostCents, &expiry, &current.Status, &current.Notes, &version, &sealed); err != nil {
		return nil, err
	}
	if err = manualResourceScopeAfterWait(ctx, tx, actor, id, customer); err != nil {
		return nil, err
	}
	if replay {
		return out, manualResourceCommit(ctx, tx, actor)
	}
	if version != in.ExpectedVersion {
		return nil, manualResourceError(409, "manual_resource_version_conflict", "人工资源已变更，请刷新后重试")
	}
	if customer != 0 && (in.ResourceType != nil || in.Address != nil || in.UpstreamID != nil || in.Username != nil || in.Password != nil) {
		return nil, manualResourceError(409, "manual_resource_bound_identity", "已绑定资源不能修改交付标识或凭据")
	}
	if customer != 0 && in.Status != nil {
		return nil, manualResourceError(409, "manual_resource_bound_status", "已绑定资源必须先终止服务并显式释放，才能变更库存状态")
	}
	if current.Status == "retired" && in.Status != nil && *in.Status != "retired" {
		return nil, manualResourceError(409, "manual_resource_retired", "已退役资源不能重新启用")
	}
	if expiry != nil {
		current.UpstreamExpiresAt = expiry.Format(time.RFC3339)
	}
	for _, pair := range []struct {
		target *string
		source *string
	}{{&current.Name, in.Name}, {&current.ResourceType, in.ResourceType}, {&current.Provider, in.Provider}, {&current.Region, in.Region}, {&current.Address, in.Address}, {&current.UpstreamID, in.UpstreamID}, {&current.UpstreamExpiresAt, in.UpstreamExpiresAt}, {&current.Status, in.Status}, {&current.Notes, in.Notes}} {
		if pair.source != nil {
			*pair.target = *pair.source
		}
	}
	if in.CostCents != nil {
		current.CostCents = *in.CostCents
	}
	if err = normalizeManualResource(&current); err != nil {
		return nil, err
	}
	if in.Username != nil || in.Password != nil {
		secret, e := manualResourceUnseal(s.ProvisionEncryptionKey, id, sealed)
		if e != nil {
			return nil, e
		}
		if in.Username != nil {
			secret.Username = strings.TrimSpace(*in.Username)
		}
		if in.Password != nil {
			secret.Password = *in.Password
		}
		if len(secret.Username) > 256 || len(secret.Password) > 4096 {
			return nil, manualResourceError(400, "invalid_manual_resource", "人工资源凭据过长")
		}
		sealed, err = manualResourceSeal(s.ProvisionEncryptionKey, id, secret)
		if err != nil {
			return nil, err
		}
	}
	expiry, _ = manualResourceExpiry(current.UpstreamExpiresAt)
	if err = tx.QueryRow(ctx, `UPDATE manual_resources SET name=$2,resource_type=$3,provider=$4,region=$5,address=$6,upstream_id=$7,cost_cents=$8,
 upstream_expires_at=$9,status=$10,notes=$11,credentials_encrypted=$12,version=version+1,updated_at=now() WHERE id=$1 RETURNING version`, id, current.Name, current.ResourceType, current.Provider, current.Region, current.Address, current.UpstreamID, current.CostCents, expiry, current.Status, current.Notes, sealed).Scan(&version); err != nil {
		return nil, err
	}
	out = map[string]any{"id": id, "version": version, "replayed": false}
	if err = Audit(ctx, tx, actor, "manual_resource.updated", "manual_resource", id, map[string]any{"version": version, "status": current.Status, "cost_cents": current.CostCents, "credentials_changed": in.Username != nil || in.Password != nil}); err != nil {
		return nil, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, manualResourceCommit(ctx, tx, actor)
}

// BindManualResourceForDelivery runs only inside the delivery transaction, after
// its RBAC/account/credential, customer, service and job locks. It deliberately
// never changes billing expiry, money, product stock or the service itself.
func (s *Store) BindManualResourceForDelivery(ctx context.Context, tx pgx.Tx, actor, resourceID, serviceID, expectedVersion int64) (ManualResourceDelivery, error) {
	var out ManualResourceDelivery
	// Unassigned inventory is deliberately hidden from customer-scoped staff,
	// including a guessed identifier submitted directly to manual delivery.
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND a.customer_scope='all' AND r.active
 AND 'resources.view'=ANY(r.permissions) AND 'resources.manage'=ANY(r.permissions))))`, actor).Scan(&allowed); err != nil {
		return out, err
	}
	if !allowed {
		return out, manualResourceError(404, "not_found", "人工资源不存在")
	}
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return out, manualResourceError(401, "unauthorized", "需要有效管理员凭据")
	}
	var status string
	var sealed []byte
	var version int64
	if resourceID < 1 || expectedVersion < 1 {
		return out, manualResourceError(409, "manual_resource_version_required", "请选择资源并提交当前资源版本")
	}
	err := tx.QueryRow(ctx, "SELECT status,upstream_id,address,credentials_encrypted,version FROM manual_resources WHERE id=$1 FOR UPDATE", resourceID).Scan(&status, &out.UpstreamID, &out.Address, &sealed, &version)
	if err == pgx.ErrNoRows {
		return out, manualResourceError(404, "not_found", "人工资源不存在")
	}
	if err != nil {
		return out, err
	}
	if version != expectedVersion {
		return out, manualResourceError(409, "manual_resource_version_conflict", "人工资源已变更，请刷新后重试")
	}
	var bound bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM manual_resource_bindings WHERE released_at IS NULL AND (resource_id=$1 OR service_id=$2))", resourceID, serviceID).Scan(&bound); err != nil {
		return out, err
	}
	if status != "available" || bound {
		return out, manualResourceError(409, "manual_resource_unavailable", "资源不可用或资源/服务已有有效绑定")
	}
	if err = RecheckManualResourceMutation(ctx, tx, actor); err != nil {
		return out, err
	}
	if out.UpstreamID == "" {
		return out, manualResourceError(409, "manual_resource_identity_required", "资源须填写实际资源标识后才能交付")
	}
	secret, err := manualResourceUnseal(s.ProvisionEncryptionKey, resourceID, sealed)
	if err != nil {
		return out, err
	}
	out.Username = secret.Username
	out.Credentials.Password = secret.Password
	if _, err = tx.Exec(ctx, "INSERT INTO manual_resource_bindings(resource_id,service_id,bound_by) VALUES($1,$2,$3)", resourceID, serviceID, actor); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, "UPDATE manual_resources SET version=version+1,updated_at=now() WHERE id=$1 RETURNING version", resourceID).Scan(&out.ResourceVersion)
	return out, err
}

// Release is explicit and only recycles inventory after the bound service is
// terminal. The old service's address, credentials and billing facts stay intact.
func (s *Store) ReleaseManualResource(ctx context.Context, actor, id int64, key string, in ManualResourceReleaseInput) (map[string]any, error) {
	key, err := manualResourceKey(key)
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion < 1 {
		return nil, manualResourceError(409, "manual_resource_version_required", "请提交人工资源当前版本")
	}
	in.Note = strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(in.Note) < 3 || utf8.RuneCountInString(in.Note) > 2000 {
		return nil, manualResourceError(400, "invalid_manual_resource_release", "请填写3至2000字符的资源释放说明")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = LockManualResourceMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	operation := fmt.Sprintf("manual_resource.release:%d", id)
	// Read only the target of this actor's own receipt before acquiring scope.
	// Actual replay/hash checking still happens through the unified receipt lock.
	var serviceID, bindingID int64
	err = tx.QueryRow(ctx, `SELECT (result->>'service_id')::bigint,(result->>'binding_id')::bigint FROM mutation_operations
 WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3`, actor, "admin:"+operation, key).Scan(&serviceID, &bindingID)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, "SELECT service_id,id FROM manual_resource_bindings WHERE resource_id=$1 AND released_at IS NULL", id).Scan(&serviceID, &bindingID)
		if err == pgx.ErrNoRows {
			if _, e := manualResourceScope(ctx, tx, actor, id); e != nil {
				return nil, e
			}
			return nil, manualResourceError(409, "manual_resource_not_bound", "资源当前没有有效绑定")
		}
	}
	if err != nil {
		return nil, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "service", serviceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil, manualResourceError(404, "not_found", "人工资源不存在")
		}
		return nil, err
	}
	hash, out, replay, err := s.manualResourceReceipt(ctx, tx, actor, operation, key, in, false)
	if err != nil {
		return nil, err
	}
	var mode, status string
	if err = tx.QueryRow(ctx, "SELECT provision_mode,status FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&mode, &status); err != nil {
		return nil, err
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM manual_resources WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		return nil, err
	}
	if err = RecheckManualResourceMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	if replay {
		var historical bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM manual_resource_bindings WHERE id=$1 AND resource_id=$2 AND service_id=$3 AND released_at IS NOT NULL)", bindingID, id, serviceID).Scan(&historical); err != nil {
			return nil, err
		}
		if !historical {
			return nil, manualResourceError(409, "manual_resource_binding_changed", "资源释放历史不匹配")
		}
		return out, manualResourceCommit(ctx, tx, actor)
	}
	if version != in.ExpectedVersion {
		return nil, manualResourceError(409, "manual_resource_version_conflict", "人工资源已变更，请刷新后重试")
	}
	var liveBinding int64
	if err = tx.QueryRow(ctx, "SELECT id FROM manual_resource_bindings WHERE resource_id=$1 AND service_id=$2 AND released_at IS NULL FOR UPDATE", id, serviceID).Scan(&liveBinding); err == pgx.ErrNoRows {
		return nil, manualResourceError(409, "manual_resource_binding_changed", "人工资源绑定已变更，请刷新后重试")
	}
	if err != nil {
		return nil, err
	}
	if liveBinding != bindingID {
		return nil, manualResourceError(409, "manual_resource_binding_changed", "人工资源绑定已变更，请刷新后重试")
	}
	if mode != "manual" || status != "terminated" {
		return nil, manualResourceError(409, "manual_resource_release_requires_termination", "请先完成原人工服务终止，再释放库存")
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND
 (status NOT IN ('succeeded','cancelled','failed') OR (status='failed' AND external_started_at IS NOT NULL)))`, serviceID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, manualResourceError(409, "manual_resource_release_pending_job", "服务仍有未决交付任务，请先核验完成")
	}
	if _, err = tx.Exec(ctx, "UPDATE manual_resource_bindings SET released_at=now(),released_by=$2,release_note=$3 WHERE id=$1", bindingID, actor, in.Note); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, "UPDATE manual_resources SET status='available',version=version+1,updated_at=now() WHERE id=$1 RETURNING version", id).Scan(&version); err != nil {
		return nil, err
	}
	out = map[string]any{"id": id, "version": version, "service_id": serviceID, "binding_id": bindingID, "replayed": false}
	if err = Audit(ctx, tx, actor, "manual_resource.released", "manual_resource", id, map[string]any{"service_id": serviceID, "binding_id": bindingID, "version": version, "note": in.Note}); err != nil {
		return nil, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, out); err != nil {
		return nil, err
	}
	return out, manualResourceCommit(ctx, tx, actor)
}
