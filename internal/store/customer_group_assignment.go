package store

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// GroupIDPresent distinguishes a missing field from an intentional null clear.
// It is input validation metadata and never participates in a valid fingerprint.
type AdminCustomerGroupAssignmentInput struct {
	GroupID         *int64 `json:"group_id"`
	ExpectedVersion int64  `json:"expected_version"`
	GroupIDPresent  bool   `json:"-"`
}

func requireCustomerGroupAssignmentCredential(ctx context.Context) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return conflict("unauthorized", "该操作需要有效的原始会话或 API 密钥")
	}
	return nil
}

func lockCustomerGroupAssignment(ctx context.Context, tx pgx.Tx, actor, customer int64, write bool) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	// Existing customer creation can hold a credential while waiting for the
	// pricing lock. Password rotation takes administrator before credentials.
	// Lock the administrator here, before pricing but without a credential, so
	// these three paths cannot form pricing -> administrator -> credential.
	if err := requireCustomerGroupAssignmentPermission(ctx, tx, actor); err != nil {
		return err
	}
	if write {
		return GroupPricingWriteLock(ctx, tx)
	}
	return GroupPricingReadLock(ctx, tx)
}

// Account/role before the original credential follows password rotation's lock
// order. Check again after every earlier pricing, receipt and membership wait.
func requireCustomerGroupAssignmentPermission(ctx context.Context, tx pgx.Tx, actor int64) error {
	if err := RequireAdministratorPermission(ctx, tx, actor, "customer_groups.manage"); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("unauthorized", "登录已失效，请重新登录")
		}
		return err
	}
	return nil
}

func requireCustomerGroupAssignmentAuthority(ctx context.Context, tx pgx.Tx, actor, customer int64) error {
	if err := requireCustomerGroupAssignmentPermission(ctx, tx, actor); err != nil {
		return err
	}
	if err := lockStorageCredential(ctx, tx, actor); err != nil {
		return err
	}
	if err := LockAdminCustomer(ctx, tx, actor, customer); err != nil {
		return err
	}
	// An unchanged credential can expire while its row lock is being acquired.
	return lockStorageCredential(ctx, tx, actor)
}

func (s *Store) AdminCustomerGroupMembership(ctx context.Context, actor, customer int64) (map[string]any, error) {
	if err := requireCustomerGroupAssignmentCredential(ctx); err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockCustomerGroupAssignment(ctx, tx, actor, customer, false); err != nil {
		return nil, err
	}
	if err = requireCustomerGroupAssignmentAuthority(ctx, tx, actor, customer); err != nil {
		return nil, err
	}
	var version int64
	var group *int64
	if err = tx.QueryRow(ctx, `SELECT v.version,m.group_id FROM customer_group_membership_versions v
 LEFT JOIN customer_group_members m ON m.customer_id=v.customer_id
 WHERE v.customer_id=$2 AND admin_customer_allowed($1,v.customer_id)`, actor, customer).Scan(&version, &group); err != nil {
		return nil, err
	}
	if err = lockStorageCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"customer_id": customer, "group_id": group, "group_membership_version": version}, nil
}

// AssignAdminCustomerGroup records a single actor/customer/key intention in the
// unified receipt table. An unknown result is recovered only with its original
// body/key; a current GET version is not evidence about that historical request.
func (s *Store) AssignAdminCustomerGroup(ctx context.Context, actor, customer int64, key string, in AdminCustomerGroupAssignmentInput) (map[string]any, error) {
	if err := requireCustomerGroupAssignmentCredential(ctx); err != nil {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockCustomerGroupAssignment(ctx, tx, actor, customer, true); err != nil {
		return nil, err
	}
	if !in.GroupIDPresent || (in.GroupID != nil && *in.GroupID < 1) {
		return nil, &AccountBusinessError{400, "invalid_group", "请显式提交有效客户分组，清空请提交 null"}
	}
	if in.ExpectedVersion < 1 {
		return nil, &AccountBusinessError{409, "group_membership_version_required", "请提交当前客户分组成员版本"}
	}
	if len(key) < 8 || len(key) > 128 {
		return nil, &AccountBusinessError{400, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键"}
	}
	operation := "customer_group.assign:" + strconv.FormatInt(customer, 10)
	// Receipt locking precedes concrete authority/credential locks. Authorization
	// is refreshed before the saved result is inspected or a new mutation starts.
	if err = lockOperationKey(ctx, tx, "mutation", strconv.FormatInt(actor, 10)+":admin:"+operation+":"+key); err != nil {
		return nil, err
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_group_membership_versions WHERE customer_id=$1 FOR UPDATE", customer).Scan(&version); err != nil {
		return nil, err
	}
	if err = requireCustomerGroupAssignmentAuthority(ctx, tx, actor, customer); err != nil {
		return nil, err
	}
	var out map[string]any
	hash, replay, err := CreationReceipt(ctx, tx, actor, operation, key, in, &out)
	if err != nil {
		return nil, err
	}
	if replay {
		out["replayed"] = true
		if err = lockStorageCredential(ctx, tx, actor); err != nil {
			return nil, err
		}
		return out, tx.Commit(ctx)
	}
	if version != in.ExpectedVersion {
		return nil, conflict("version_conflict", "客户分组已变更，请重新加载后核对")
	}
	var previous *int64
	if err = tx.QueryRow(ctx, "SELECT (SELECT group_id FROM customer_group_members WHERE customer_id=$1)", customer).Scan(&previous); err != nil {
		return nil, err
	}
	if in.GroupID != nil {
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM customer_groups WHERE id=$1)", *in.GroupID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, &AccountBusinessError{400, "invalid_group", "客户分组不存在"}
		}
		_, err = tx.Exec(ctx, `INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2)
 ON CONFLICT(customer_id) DO UPDATE SET group_id=EXCLUDED.group_id`, customer, *in.GroupID)
	} else {
		_, err = tx.Exec(ctx, "DELETE FROM customer_group_members WHERE customer_id=$1", customer)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_group_membership_versions WHERE customer_id=$1", customer).Scan(&version); err != nil {
		return nil, err
	}
	out = map[string]any{"customer_id": customer, "group_id": in.GroupID, "group_membership_version": version, "replayed": false}
	if err = Audit(ctx, tx, actor, "customer_group.assigned", "user", customer, map[string]any{"previous_group_id": previous, "group_id": in.GroupID, "expected_version": in.ExpectedVersion, "group_membership_version": version}); err != nil {
		return nil, err
	}
	if err = SaveCreationReceipt(ctx, tx, actor, operation, key, hash, out); err != nil {
		return nil, err
	}
	// Audit/receipt triggers can also wait long enough for natural expiry.
	if err = lockStorageCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
