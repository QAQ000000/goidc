package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type AdminCreateCustomerInput struct {
	Username, Email, PasswordHash, DisplayName, Status string
	GroupID, SalesOwnerID                              *int64
	Profile                                            CustomerProfileInput
}
type AdminUpdateCustomerInput struct {
	DisplayName     *string                    `json:"display_name"`
	Email           *string                    `json:"email"`
	Status          *string                    `json:"status"`
	Profile         map[string]json.RawMessage `json:"profile"`
	ExpectedVersion int64                      `json:"expected_version"`
}

func lockCustomerAccountMutation(ctx context.Context, tx pgx.Tx, actor int64, operation, key string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("customer-mutation:%d:%s:%s", actor, operation, key))
	return err
}
func saveCustomerAccountOperation(ctx context.Context, tx pgx.Tx, actor int64, operation, key, hash string, result map[string]any) error {
	return SaveOperation(ctx, tx, actor, "admin:"+operation, key, hash, result)
}
func customerAccountProfileError(err error) error {
	if err != nil && err.Error() == "invalid_custom_field" {
		return &AccountBusinessError{Status: 400, Code: "invalid_custom_field", Message: "客户自定义字段值无效"}
	}
	if err != nil && err.Error() == "stale_customer_profile" {
		return &AccountBusinessError{Status: 409, Code: "stale_customer_profile", Message: "客户资料已更新，请刷新后重试"}
	}
	return err
}
func (s *Store) AdminCreateCustomer(ctx context.Context, actor int64, input AdminCreateCustomerInput, key, payloadHash string) (map[string]any, error) {
	username, email, displayName, status, hash := input.Username, input.Email, input.DisplayName, input.Status, input.PasswordHash
	profile := input.Profile
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	super, groupAllowed, salesEnabled, err := LockCustomerManager(ctx, tx, actor)
	if err != nil {
		return nil, err
	}
	if (input.GroupID != nil && !groupAllowed) || (input.SalesOwnerID != nil && !super) {
		return nil, &AccountBusinessError{Status: 403, Code: "permission_denied", Message: "没有设置客户分组或销售归属的权限"}
	}
	if (input.GroupID != nil && *input.GroupID < 1) || (input.SalesOwnerID != nil && *input.SalesOwnerID < 1) {
		return nil, &AccountBusinessError{Status: 400, Code: "invalid_assignment", Message: "客户分组或销售归属无效"}
	}
	if err = lockCustomerAccountMutation(ctx, tx, actor, "customer.create", key); err != nil {
		return nil, err
	}
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, "customer.create", key, payloadHash); e != nil {
		return nil, e
	} else if replayed {
		customerID := int64(replay["id"].(float64))
		if e = LockAdminCustomer(ctx, tx, actor, customerID); e != nil {
			return nil, e
		}
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return replay, nil
	}
	if input.GroupID != nil {
		var groupID int64
		if err = tx.QueryRow(ctx, "SELECT id FROM customer_groups WHERE id=$1 FOR SHARE", *input.GroupID).Scan(&groupID); err != nil {
			if err == pgx.ErrNoRows {
				return nil, &AccountBusinessError{Status: 400, Code: "invalid_group", Message: "客户分组不存在"}
			} else {
				return nil, err
			}
		}
	}
	var conflict bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE lower(username)=$1 OR lower(email)=$2)", username, email).Scan(&conflict); err != nil {
		return nil, err
	}
	if conflict {
		return nil, &AccountBusinessError{Status: 409, Code: "user_conflict", Message: "用户名或邮箱已被使用"}
	}

	var salesOwnerID *int64
	if input.SalesOwnerID != nil && *input.SalesOwnerID > 0 {
		var ownerExists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM administrators WHERE id=$1 AND role='admin' AND status='active' AND sales_enabled)", *input.SalesOwnerID).Scan(&ownerExists); err != nil {
			return nil, err
		}
		if !ownerExists {
			return nil, &AccountBusinessError{Status: 400, Code: "invalid_sales_owner", Message: "销售负责人不存在或已停用"}
		}
		salesOwnerID = input.SalesOwnerID
	} else if !super && salesEnabled {
		salesOwnerID = &actor
	}
	var scopeAllowed bool
	if err = tx.QueryRow(ctx, "SELECT admin_customer_owner_allowed($1,$2)", actor, salesOwnerID).Scan(&scopeAllowed); err != nil {
		return nil, err
	}
	if !scopeAllowed {
		return nil, &AccountBusinessError{Status: 403, Code: "permission_denied", Message: "当前客户范围不允许创建无归属客户，请联系管理员开启销售身份"}
	}

	var id int64
	if err = tx.QueryRow(ctx,
		"INSERT INTO users(username,email,password_hash,display_name,role,status,sales_owner_id,email_verified) VALUES($1,$2,$3,$4,'customer',$5,$6,true) RETURNING id",
		username, email, string(hash), displayName, status, salesOwnerID).Scan(&id); err != nil {
		return nil, err
	}

	if _, err = tx.Exec(ctx, "INSERT INTO carts(user_id) VALUES($1)", id); err != nil {
		return nil, err
	}

	if input.GroupID != nil && *input.GroupID > 0 {
		if _, err = tx.Exec(ctx, "INSERT INTO customer_group_members(customer_id, group_id) VALUES($1, $2) ON CONFLICT DO NOTHING", id, *input.GroupID); err != nil {
			return nil, err
		}
	}

	if err = saveCustomerProfile(ctx, tx, id, profile); err != nil {
		return nil, customerAccountProfileError(err)
	}
	companyName := strings.TrimSpace(profile.CompanyName)
	phone := strings.TrimSpace(profile.Phone)
	if companyName != "" || phone != "" {
		if _, err = tx.Exec(ctx,
			"INSERT INTO customer_contacts(customer_id, username, email, company_name, phone_number, status) VALUES($1, $2, $3, $4, $5, 'active') ON CONFLICT DO NOTHING",
			id, displayName, email, companyName, phone); err != nil {
			return nil, err
		}
	}

	if err = Audit(ctx, tx, actor, "customer.created", "user", id, map[string]any{
		"username":     username,
		"email":        email,
		"display_name": displayName,
		"status":       status,
	}); err != nil {
		return nil, err
	}

	result := map[string]any{"id": id, "username": username, "email": email, "display_name": displayName, "status": status, "profile_version": 1, "replayed": false}
	if err = saveCustomerAccountOperation(ctx, tx, actor, "customer.create", key, payloadHash, result); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) AdminUpdateCustomer(ctx context.Context, actor, id int64, input AdminUpdateCustomerInput, rawKey string) (map[string]any, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = LockCustomerManager(ctx, tx, actor); err != nil {
		return nil, err
	}
	if err = LockAdminCustomer(ctx, tx, actor, id); err != nil {
		return nil, err
	}
	// Resolve the customer scope before validating concurrency or idempotency
	// input, so a caller cannot use malformed/missing fields to probe records.
	if value, exists := input.Profile["expected_version"]; exists {
		var nested int64
		if err := json.Unmarshal(value, &nested); err != nil || (input.ExpectedVersion > 0 && input.ExpectedVersion != nested) {
			return nil, &AccountBusinessError{Status: 400, Code: "invalid_profile", Message: "资料版本无效"}
		}
		input.ExpectedVersion = nested
	}
	if input.ExpectedVersion < 1 {
		return nil, &AccountBusinessError{Status: 409, Code: "customer_version_required", Message: "请提交当前客户资料版本"}
	}
	key := strings.TrimSpace(rawKey)
	if len(key) < 8 || len(key) > 128 {
		return nil, &AccountBusinessError{Status: 400, Code: "invalid_idempotency_key", Message: "请提供长度为 8 到 128 的幂等键"}
	}
	payloadHash := accountBusinessHash(struct {
		ID    int64
		Input any
	}{id, input})
	operation := "customer.update:" + strconv.FormatInt(id, 10)
	if err = lockCustomerAccountMutation(ctx, tx, actor, operation, key); err != nil {
		return nil, err
	}
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, payloadHash); e != nil {
		return nil, e
	} else if replayed {
		if e = tx.Commit(ctx); e != nil {
			return nil, e
		}
		return replay, nil
	}
	var currentStatus string
	if err = tx.QueryRow(ctx, "SELECT status FROM users WHERE id=$1 AND role='customer' FOR UPDATE", id).Scan(&currentStatus); err != nil {
		return nil, err
	}
	profile, err := currentCustomerAccountProfile(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if profile.ExpectedVersion != input.ExpectedVersion {
		return nil, &AccountBusinessError{Status: 409, Code: "stale_customer_profile", Message: "客户资料已更新，请刷新后重试"}
	}
	if input.Profile != nil {
		if value, exists := input.Profile["custom_fields"]; exists && strings.TrimSpace(string(value)) == "null" {
			return nil, &AccountBusinessError{Status: 400, Code: "invalid_custom_field", Message: "客户字段不能为null"}
		}
		// Merge only explicitly submitted profile properties, retaining existing data.
		raw, _ := json.Marshal(profile)
		var merged map[string]json.RawMessage
		_ = json.Unmarshal(raw, &merged)
		for k, v := range input.Profile {
			if strings.TrimSpace(string(v)) == "null" {
				return nil, &AccountBusinessError{Status: 400, Code: "invalid_profile", Message: "客户资料字段不能为null"}
			}
			if _, known := merged[k]; !known {
				return nil, &AccountBusinessError{Status: 400, Code: "invalid_profile", Message: "未知客户资料字段"}
			}
			merged[k] = v
		}
		raw, _ = json.Marshal(merged)
		if err = json.Unmarshal(raw, &profile); err != nil || !profile.Valid() {
			return nil, &AccountBusinessError{Status: 400, Code: "invalid_profile", Message: "客户资料格式或长度无效"}
		}
		// An omitted custom_fields means preserve, but still validate required values.
		if _, present := input.Profile["custom_fields"]; !present {
			profile.CustomFields = nil
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET display_name=COALESCE($2,display_name),email=COALESCE($3,email),status=COALESCE($4,status),updated_at=now() WHERE id=$1`, id, input.DisplayName, input.Email, input.Status); err != nil {
		return nil, err
	}
	if input.Status != nil && *input.Status == "disabled" {
		if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
			return nil, err
		}
	}
	if input.Profile != nil {
		if err = saveCustomerProfile(ctx, tx, id, profile); err != nil {
			return nil, customerAccountProfileError(err)
		}
	}
	if err = Audit(ctx, tx, actor, "customer.updated", "user", id, map[string]any{"display_name": input.DisplayName, "email": input.Email, "status": input.Status, "previous_status": currentStatus, "profile_updated": input.Profile != nil}); err != nil {
		return nil, err
	}
	if input.Profile == nil {
		if _, err = tx.Exec(ctx, "UPDATE customer_profiles SET version=version+1,updated_at=now() WHERE customer_id=$1", id); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"user_id": id, "profile_version": input.ExpectedVersion + 1, "replayed": false}
	if err = saveCustomerAccountOperation(ctx, tx, actor, operation, key, payloadHash, result); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
