package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type CustomerFileInput struct {
	CustomerID      int64  `json:"customer_id"`
	Title           string `json:"title"`
	Remarks         string `json:"remarks"`
	ExpectedVersion int64  `json:"expected_version"`
}
type CustomerFileVersion struct {
	ID              int64 `json:"id"`
	ExpectedVersion int64 `json:"expected_version"`
}

// Serialize ownership moves with list/download/delete. Sales assignment takes its
// own exclusive customer lock; acquire shared customer locks in numeric order.
func (s *Store) CustomerFileTx(ctx context.Context, actorID int64, admin, manage bool) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err == nil {
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108198)")
	}
	if err == nil {
		_, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108199)")
	}
	if err == nil && admin {
		var allowed bool
		permission := "customer_files.view"
		if manage {
			permission = "customer_files.manage"
		}
		err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'customer_files.view'=ANY(r.permissions) AND $2=ANY(r.permissions)),false)
   FROM administrators u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID, permission).Scan(&allowed)
		if err == nil && !allowed {
			err = conflict("permission_denied", "没有访问客户文件的权限")
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func LockCustomerFileOwners(ctx context.Context, tx pgx.Tx, actorID int64, owners ...int64) error {
	owners = slices.Clone(owners)
	slices.Sort(owners)
	owners = slices.Compact(owners)
	for _, owner := range owners {
		if err := LockAdminCustomer(ctx, tx, actorID, owner); err != nil {
			return err
		}
	}
	return nil
}
func SaveCustomerFile(ctx context.Context, tx pgx.Tx, actorID, id int64, in CustomerFileInput, file *DownloadFile) (int64, error) {
	return (&Store{}).SaveCustomerFile(ctx, tx, actorID, id, in, file)
}

func (s *Store) SaveCustomerFile(ctx context.Context, tx pgx.Tx, actorID, id int64, in CustomerFileInput, file *DownloadFile) (int64, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Remarks = strings.TrimSpace(in.Remarks)
	if in.CustomerID < 1 || in.Title == "" || !contentTextValid(in.Title, 255, false) || !contentTextValid(in.Remarks, 10000, true) {
		return 0, conflict("invalid_customer_file", "客户、文件名称或备注无效")
	}
	if file != nil && (file.Filename == "" || file.MIME == "" || !contentTextValid(file.Filename, 255, false) || strings.ContainsAny(file.Filename, "/\\") || len(file.Content) == 0 || len(file.Content) > 50<<20) {
		return 0, conflict("invalid_customer_file", "上传文件无效")
	}
	var previousOwner, current int64
	if id != 0 {
		if err := tx.QueryRow(ctx, "SELECT customer_id,version FROM customer_files WHERE id=$1", id).Scan(&previousOwner, &current); err != nil {
			return 0, err
		}
	}
	owners := []int64{in.CustomerID}
	if previousOwner > 0 {
		owners = append(owners, previousOwner)
	}
	if err := LockCustomerFileOwners(ctx, tx, actorID, owners...); err != nil {
		return 0, err
	}
	if id != 0 && current != in.ExpectedVersion {
		return 0, conflict("version_conflict", "客户文件已变更，请重新加载")
	}
	if id == 0 && file == nil {
		return 0, conflict("download_file_required", "请选择上传文件")
	}
	var hash string
	var stored FilePayload
	if file != nil {
		digest := sha256.Sum256(file.Content)
		hash = hex.EncodeToString(digest[:])
		var err error
		stored, err = s.PersistFile(ctx, tx, "customer_files", file.Content)
		if err != nil {
			return 0, err
		}
		if err = LockFileCredential(ctx, tx, actorID); err != nil {
			return 0, err
		}
	}
	var err error
	action := "customer_file.created"
	if id == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO customer_files(customer_id,title,remarks,filename,mime_type,size_bytes,sha256,content,uploader_id,storage_object_id)
   VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, in.CustomerID, in.Title, in.Remarks, file.Filename, file.MIME, len(file.Content), hash, stored.Content, actorID, stored.ObjectID).Scan(&id)
	} else {
		action = "customer_file.updated"
		if file == nil {
			_, err = tx.Exec(ctx, "UPDATE customer_files SET customer_id=$2,title=$3,remarks=$4,version=version+1,updated_at=now() WHERE id=$1", id, in.CustomerID, in.Title, in.Remarks)
		} else {
			_, err = tx.Exec(ctx, `UPDATE customer_files SET customer_id=$2,title=$3,remarks=$4,filename=$5,mime_type=$6,size_bytes=$7,sha256=$8,content=$9,uploader_id=$10,storage_object_id=$11,version=version+1,updated_at=now() WHERE id=$1`, id, in.CustomerID, in.Title, in.Remarks, file.Filename, file.MIME, len(file.Content), hash, stored.Content, actorID, stored.ObjectID)
		}
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actorID, action, "customer_file", id, map[string]any{"customer_id": in.CustomerID, "previous_customer_id": previousOwner, "title": in.Title, "file_replaced": file != nil, "sha256": hash, "version": current + 1}); err != nil {
		return 0, err
	}
	return id, nil
}
func DeleteCustomerFiles(ctx context.Context, tx pgx.Tx, actorID int64, items []CustomerFileVersion) error {
	if len(items) == 0 || len(items) > 100 {
		return conflict("invalid_customer_files", "请选择 1 至 100 个文件")
	}
	slices.SortFunc(items, func(a, b CustomerFileVersion) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	owners := []int64{}
	previous := int64(0)
	for _, item := range items {
		if item.ID < 1 || item.ExpectedVersion < 1 || previous == item.ID {
			return conflict("invalid_customer_files", "文件编号重复或版本无效")
		}
		previous = item.ID
		var owner int64
		if err := tx.QueryRow(ctx, "SELECT customer_id FROM customer_files WHERE id=$1", item.ID).Scan(&owner); err != nil {
			return err
		}
		owners = append(owners, owner)
	}
	if err := LockCustomerFileOwners(ctx, tx, actorID, owners...); err != nil {
		return err
	}
	for i, item := range items {
		var version int64
		var title string
		if err := tx.QueryRow(ctx, "SELECT version,title FROM customer_files WHERE id=$1 FOR UPDATE", item.ID).Scan(&version, &title); err != nil {
			return err
		}
		if version != item.ExpectedVersion {
			return conflict("version_conflict", "客户文件已变更，请重新加载")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM customer_files WHERE id=$1", item.ID); err != nil {
			return err
		}
		if err := Audit(ctx, tx, actorID, "customer_file.deleted", "customer_file", item.ID, map[string]any{"customer_id": owners[i], "title": title, "version": version}); err != nil {
			return err
		}
	}
	return nil
}
