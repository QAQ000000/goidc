package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/codex/zjmfgo/internal/supplier"
)

const supplierAccountColumns = `a.id,a.name,a.provider,a.base_url,a.username,a.active,a.version,a.running_job_id,a.config,a.schema_version,a.credential_format,a.credentials_encrypted,a.secure_fields,a.identity_key`

// Every supplier runtime, including probes, uses this row decoder. Credential
// formats are explicit; corrupt named data must never fall back to a legacy key.
type supplierAccountRecord struct {
	item             SupplierAccountSummary
	credentialFormat int
	legacy           []byte
	fields           map[string][]byte
	identity         string
	malformed        bool
}

func scanSupplierAccount(row interface{ Scan(...any) error }, extra ...any) (supplierAccountRecord, error) {
	var rec supplierAccountRecord
	var config, fields []byte
	i := &rec.item
	args := []any{&i.ID, &i.Name, &i.Provider, &i.BaseURL, &i.Username, &i.Active, &i.Version, &i.RunningJobID, &config, &i.SchemaVersion, &rec.credentialFormat, &rec.legacy, &fields, &rec.identity}
	err := row.Scan(append(args, extra...)...)
	if err != nil {
		return rec, err
	}
	i.BlockedJobID = i.RunningJobID
	if json.Unmarshal(config, &i.Config) != nil || i.Config == nil || json.Unmarshal(fields, &rec.fields) != nil || rec.fields == nil || len(config) > 128<<10 || len(fields) > 256<<10 {
		rec.malformed = true
	}
	if i.Config == nil {
		i.Config = map[string]any{}
	}
	if rec.fields == nil {
		rec.fields = map[string][]byte{}
	}
	if rec.credentialFormat != 0 && rec.credentialFormat != 1 || rec.credentialFormat == 0 && len(rec.fields) != 0 {
		rec.malformed = true
	}
	return rec, nil
}

func supplierCredentialFlags(rec supplierAccountRecord, desc channel.Descriptor) (map[string]bool, bool) {
	flags := map[string]bool{}
	known := map[string]bool{}
	for _, key := range channel.SecretKeys(desc) {
		known[key] = true
		flags[key] = false
	}
	if rec.credentialFormat == 0 {
		// Only the original account format ever stored this unnamed password.
		valid := rec.item.Provider == supplier.ProviderZJMF && rec.item.SchemaVersion == 1 && known["secret"] && len(rec.fields) == 0
		flags["secret"] = len(rec.legacy) > 0 && valid
		return flags, valid
	}
	valid := rec.credentialFormat == 1
	for key, value := range rec.fields {
		if !known[key] || len(value) < 28 || len(value) > 8192 {
			valid = false
			continue
		}
		flags[key] = true
	}
	return flags, valid
}

func describeSupplierAccount(rec supplierAccountRecord) SupplierAccountSummary {
	i := rec.item
	i.SecureFields = map[string]bool{}
	i.CredentialsConfigured = len(rec.legacy) > 0 && rec.credentialFormat == 0 || len(rec.fields) > 0 && rec.credentialFormat == 1
	definition, ok := supplier.Lookup(i.Provider)
	if !ok {
		i.ConfigurationStatus, i.Config = "implementation_missing", map[string]any{}
		return i
	}
	desc := definition.Descriptor
	i.FactoryAvailable = desc.InstanceMode == "supplier" && channel.HasFactory(channel.Provision, desc.ID)
	var credentialsValid bool
	i.SecureFields, credentialsValid = supplierCredentialFlags(rec, desc)
	normalized, validationErrors := channel.ValidateConfiguration(desc, i.Config, i.SecureFields, i.Active)
	i.Config, i.ConfigurationErrors = normalized, validationErrors
	if !rec.malformed && len(validationErrors) == 0 && i.SchemaVersion == desc.SchemaVersion {
		if account, err := supplier.NormalizeAccount(supplier.Account{ID: i.ID, Provider: i.Provider, Values: i.Config, SchemaVersion: i.SchemaVersion}); err == nil {
			i.Config = account.Values
		} else {
			i.ConfigurationErrors = append(i.ConfigurationErrors, channel.ValidationError{Field: "config", Code: "invalid"})
		}
	}
	// Never echo a value in a credential slot, including malformed persisted data.
	for _, f := range desc.Fields {
		if f.Secret {
			delete(i.Config, f.Key)
		}
	}
	switch {
	case !i.FactoryAvailable:
		i.ConfigurationStatus, i.Config = "implementation_missing", map[string]any{}
	case i.SchemaVersion != desc.SchemaVersion:
		i.ConfigurationStatus, i.Config = "schema_incompatible", map[string]any{}
	case rec.malformed || !credentialsValid:
		i.ConfigurationStatus = "invalid"
		i.ConfigurationErrors = append(i.ConfigurationErrors, channel.ValidationError{Field: "secure_config", Code: "invalid_format"})
	case len(i.ConfigurationErrors) > 0:
		i.ConfigurationStatus = "invalid"
	default:
		i.ConfigurationStatus = "valid"
	}
	return i
}

func supplierCredentialAAD(id int64, field string) []byte {
	return []byte(fmt.Sprintf("supplier:account-field:v1:%d:%s", id, field))
}

func (s *Store) openSupplierCredentials(rec supplierAccountRecord, desc channel.Descriptor) (map[string]string, error) {
	_, valid := supplierCredentialFlags(rec, desc)
	if rec.malformed || !valid {
		return nil, conflict("supplier_credentials_invalid", "供应商加密配置格式不可用")
	}
	if len(s.ProvisionEncryptionKey) != 32 {
		return nil, conflict("supplier_encryption_unconfigured", "供应商凭据加密密钥未配置")
	}
	secrets := map[string]string{}
	if rec.credentialFormat == 0 {
		if len(rec.legacy) == 0 {
			return secrets, nil
		}
		var value string
		if err := supplierUnseal(s.ProvisionEncryptionKey, "account", rec.item.ID, rec.legacy, &value); err != nil {
			return nil, err
		}
		if value != "" {
			secrets["secret"] = value
		}
		return secrets, nil
	}
	for field, sealed := range rec.fields {
		plain, err := secure.Open(s.ProvisionEncryptionKey, supplierCredentialAAD(rec.item.ID, field), sealed)
		if err != nil || len(plain) == 0 || len(plain) > 4096 {
			return nil, conflict("supplier_credentials_invalid", "供应商加密配置不可用")
		}
		secrets[field] = string(plain)
	}
	return secrets, nil
}

func (s *Store) supplierRuntimeAccount(rec supplierAccountRecord) (supplier.Account, error) {
	i := describeSupplierAccount(rec)
	if !i.Active {
		return supplier.Account{}, conflict("supplier_disabled", "供应商已停用")
	}
	switch i.ConfigurationStatus {
	case "implementation_missing":
		return supplier.Account{}, conflict("supplier_factory_unavailable", "当前构建未提供该供应商实现")
	case "schema_incompatible":
		return supplier.Account{}, conflict("supplier_schema_incompatible", "供应商配置定义不兼容，请检查当前构建")
	case "valid":
	default:
		return supplier.Account{}, conflict("invalid_supplier_config", "供应商配置不可用，请检查字段及凭据")
	}
	definition, _ := supplier.Lookup(i.Provider)
	secrets, err := s.openSupplierCredentials(rec, definition.Descriptor)
	if err != nil {
		return supplier.Account{}, err
	}
	account, err := supplier.NormalizeAccount(supplier.Account{ID: i.ID, Provider: i.Provider, Values: i.Config, Secrets: secrets, SchemaVersion: i.SchemaVersion})
	if err != nil {
		return supplier.Account{}, conflict("invalid_supplier_config", "供应商配置不可用")
	}
	identity, err := supplier.AccountIdentity(account)
	if err != nil || identity == "" || rec.identity != "" && rec.identity != identity {
		return supplier.Account{}, conflict("supplier_identity_invalid", "供应商来源及账户身份不可核验")
	}
	return account, nil
}

func (s *Store) supplierAdapterRecord(ctx context.Context, q sqlQueryer, rec supplierAccountRecord) (supplier.Adapter, error) {
	account, err := s.supplierRuntimeAccount(rec)
	if err != nil {
		return nil, err
	}
	identity, err := supplier.AccountIdentity(account)
	if err != nil {
		return nil, conflict("supplier_identity_invalid", "供应商来源及账户身份不可核验")
	}
	if err = checkSupplierIdentity(ctx, q, account, account.ID, identity, true); err != nil {
		return nil, err
	}
	a, err := supplier.New(account)
	if err != nil {
		return nil, conflict("invalid_supplier_config", "供应商配置不可用，请检查字段及凭据")
	}
	return a, nil
}

func supplierInputConfig(in SupplierInput, old *supplierAccountRecord) (map[string]any, error) {
	values := in.Config
	if values == nil && old != nil {
		values = old.item.Config
	}
	copyValues := map[string]any{}
	for key, value := range values {
		copyValues[key] = value
	}
	if in.BaseURL != "" || in.Username != "" || in.Password != "" {
		if in.Provider != supplier.ProviderZJMF {
			return nil, conflict("invalid_supplier_config", "此供应商不支持旧版账户字段")
		}
		for key, alias := range map[string]string{"endpoint": strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"), "username": strings.TrimSpace(in.Username)} {
			if alias == "" {
				continue
			}
			if previous, exists := copyValues[key]; exists && previous != alias {
				return nil, conflict("invalid_supplier_config", "供应商新旧配置字段不一致")
			}
			copyValues[key] = alias
		}
	}
	return copyValues, nil
}

func legacySupplierRequest(in SupplierInput) bool {
	return in.Provider == supplier.ProviderZJMF && in.Config == nil && len(in.SecureConfig) == 0 && len(in.ClearSecureFields) == 0
}

func validateSupplierInput(in SupplierInput, old *supplierAccountRecord) (supplier.Account, channel.Descriptor, []byte, string, error) {
	invalid := func(code, message string) (supplier.Account, channel.Descriptor, []byte, string, error) {
		return supplier.Account{}, channel.Descriptor{}, nil, "", conflict(code, message)
	}
	definition, ok := supplier.Lookup(in.Provider)
	if !ok || definition.Descriptor.InstanceMode != "supplier" || !channel.HasFactory(channel.Provision, definition.Descriptor.ID) {
		return invalid("supplier_factory_unavailable", "当前构建未提供该供应商实现")
	}
	desc := definition.Descriptor
	legacyRequest := legacySupplierRequest(in)
	if in.SchemaVersion != desc.SchemaVersion && !(in.SchemaVersion == 0 && legacyRequest) {
		return invalid("supplier_schema_incompatible", "供应商配置定义已变更，请重新加载")
	}
	if old != nil {
		if old.item.SchemaVersion != desc.SchemaVersion || in.ExpectedSchemaVersion != old.item.SchemaVersion && !(in.ExpectedSchemaVersion == 0 && legacyRequest) {
			return invalid("supplier_schema_incompatible", "供应商配置定义已变更，请重新加载")
		}
		if old.malformed {
			return invalid("invalid_supplier_config", "供应商配置格式不可用，只能停用")
		}
	}
	values, err := supplierInputConfig(in, old)
	if err != nil {
		return supplier.Account{}, channel.Descriptor{}, nil, "", err
	}
	account, err := supplier.NormalizeAccount(supplier.Account{Provider: in.Provider, Values: values, SchemaVersion: desc.SchemaVersion})
	if err != nil {
		return invalid("invalid_supplier_config", "供应商配置字段、类型或取值无效")
	}
	raw, err := json.Marshal(account.Values)
	if err != nil || len(raw) > 128<<10 {
		return invalid("invalid_supplier_config", "供应商配置过长或无效")
	}
	if old != nil {
		previous, e := supplier.NormalizeAccount(supplier.Account{Provider: old.item.Provider, Values: old.item.Config, SchemaVersion: old.item.SchemaVersion})
		if e != nil {
			return invalid("invalid_supplier_config", "已有供应商配置不可用，只能停用")
		}
		previousJSON, _ := json.Marshal(previous.Values)
		if string(previousJSON) != string(raw) {
			return invalid("supplier_identity_locked", "供应商普通配置创建后不可更改，请使用原配置")
		}
	}
	identity, err := supplier.AccountIdentity(account)
	if err != nil || len(identity) == 0 || len(identity) > 256 || old != nil && old.identity != "" && old.identity != identity {
		return invalid("supplier_identity_invalid", "供应商来源及账户身份不可核验")
	}
	return account, desc, raw, identity, nil
}

func supplierChangedFields(in SupplierInput) []string {
	changed := map[string]bool{}
	for field, value := range in.SecureConfig {
		if value != "" {
			changed[field] = true
		}
	}
	for _, field := range in.ClearSecureFields {
		changed[field] = true
	}
	if in.Password != "" {
		changed["secret"] = true
	}
	keys := make([]string, 0, len(changed))
	for field := range changed {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) mergeSupplierCredentials(rec supplierAccountRecord, desc channel.Descriptor, in SupplierInput) ([]byte, int, map[string]string, bool, error) {
	secrets, err := s.openSupplierCredentials(rec, desc)
	if err != nil {
		return nil, 0, nil, false, err
	}
	known := map[string]bool{}
	for _, field := range channel.SecretKeys(desc) {
		known[field] = true
	}
	provided := map[string]string{}
	for field, value := range in.SecureConfig {
		if !known[field] || len(value) > 4096 {
			return nil, 0, nil, false, conflict("invalid_supplier_secret", "供应商凭据字段或长度无效")
		}
		provided[field] = value
	}
	if in.Password != "" {
		if in.Provider != supplier.ProviderZJMF || !known["secret"] || provided["secret"] != "" && provided["secret"] != in.Password {
			return nil, 0, nil, false, conflict("invalid_supplier_secret", "供应商新旧凭据字段不一致")
		}
		provided["secret"] = in.Password
	}
	clear := map[string]bool{}
	for _, field := range in.ClearSecureFields {
		if !known[field] || clear[field] || provided[field] != "" {
			return nil, 0, nil, false, conflict("invalid_supplier_secret", "供应商凭据清除字段重复或冲突")
		}
		clear[field] = true
		delete(secrets, field)
	}
	for field, value := range provided {
		if value != "" {
			secrets[field] = value
		}
	}
	flags := map[string]bool{}
	for field, value := range secrets {
		flags[field] = value != ""
	}
	if len(channel.ValidateSecrets(desc, flags, *in.Active)) > 0 || len(channel.ValidateSecretValues(desc, secrets)) > 0 {
		return nil, 0, nil, false, conflict("invalid_supplier_secret", "供应商必填凭据缺失或格式无效")
	}
	changed := len(supplierChangedFields(in)) > 0
	if !changed {
		fields, e := json.Marshal(rec.fields)
		return fields, rec.credentialFormat, secrets, false, e
	}
	// An old row retains its original ciphertext, even when new credentials
	// promote it to the explicit named format. Runtime ignores that old bytea.
	fields := map[string][]byte{}
	for field, value := range secrets {
		sealed, e := secure.Seal(s.ProvisionEncryptionKey, supplierCredentialAAD(rec.item.ID, field), []byte(value))
		if e != nil {
			return nil, 0, nil, false, e
		}
		fields[field] = sealed
	}
	raw, err := json.Marshal(fields)
	return raw, 1, secrets, true, err
}

// All catalogue management writes hold CatalogTx's global lock. The legacy
// scan closes the uniqueness gap before an old row has an identity_key, and
// includes disabled rows so an account cannot evade a pending shared-cart job.
func checkSupplierIdentity(ctx context.Context, q sqlQueryer, account supplier.Account, ownID int64, identity string, activeOnly bool) error {
	var duplicate bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supplier_accounts WHERE provider=$1 AND identity_key=$2 AND id<>$3 AND (NOT $4::bool OR active OR running_job_id IS NOT NULL))`, account.Provider, identity, ownID, activeOnly).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return conflict("supplier_identity_exists", "该上游账户已存在，请使用原供应商账户")
	}
	rows, err := q.Query(ctx, `SELECT id,config,schema_version FROM supplier_accounts WHERE provider=$1 AND identity_key='' AND id<>$2 AND (NOT $3::bool OR active OR running_job_id IS NOT NULL)`, account.Provider, ownID, activeOnly)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var previous supplier.Account
		var raw []byte
		if err = rows.Scan(&previous.ID, &raw, &previous.SchemaVersion); err != nil {
			return err
		}
		previous.Provider = account.Provider
		if json.Unmarshal(raw, &previous.Values) != nil {
			return conflict("supplier_identity_unavailable", "旧供应商身份无法核验，请先检查已有账户")
		}
		previous, err = supplier.NormalizeAccount(previous)
		if err != nil {
			return conflict("supplier_identity_unavailable", "旧供应商身份无法核验，请先检查已有账户")
		}
		previousIdentity, e := supplier.AccountIdentity(previous)
		if e != nil || previousIdentity == "" {
			return conflict("supplier_identity_unavailable", "旧供应商身份无法核验，请先检查已有账户")
		}
		if previousIdentity == identity {
			return conflict("supplier_identity_exists", "该上游账户已存在，请使用原供应商账户")
		}
	}
	return rows.Err()
}
