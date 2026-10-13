package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// CheckAdminCreationPermission holds the current credential and grants until commit,
// including when returning a receipt for an earlier request.
func CheckAdminCreationPermission(ctx context.Context, tx pgx.Tx, actor int64, domain string) error {
	if err := LockFileCredential(ctx, tx, actor); err != nil {
		return err
	}
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actor).Scan(&role, &roleID); err != nil {
		return err
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "没有创建权限")
	}
	var allowed bool
	if err := tx.QueryRow(ctx, "SELECT active AND $2=ANY(permissions) AND $3=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID, domain+".view", domain+".manage").Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有创建权限")
	}
	return nil
}

// CreationReceipt serializes identical submissions. Empty keys preserve the
// existing API contract; independent creation pages always supply a key.
func CreationReceipt(ctx context.Context, tx pgx.Tx, actor int64, operation, key string, payload, result any) (string, bool, error) {
	if key == "" {
		return "", false, nil
	}
	if len(key) < 8 || len(key) > 128 {
		return "", false, conflict("invalid_idempotency_key", "幂等键长度必须在 8 到 128 之间")
	}
	if err := lockOperationKey(ctx, tx, fmt.Sprintf("admin-create:%d:%s", actor, operation), key); err != nil {
		return "", false, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", false, err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var saved string
	var receipt []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM admin_setting_operations WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3", actor, operation, key).Scan(&saved, &receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return hash, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if saved != hash {
		return "", false, conflict("idempotency_conflict", "相同幂等键对应不同创建请求")
	}
	return hash, true, json.Unmarshal(receipt, result)
}

func SaveCreationReceipt(ctx context.Context, tx pgx.Tx, actor int64, operation, key, hash string, result any) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO admin_setting_operations(actor_id,operation,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,$5)", actor, operation, key, hash, raw)
	return err
}
