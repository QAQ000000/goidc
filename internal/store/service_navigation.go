package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

type ServiceGroupInput struct {
	Name            string `json:"name"`
	Icon            string `json:"icon"`
	SortOrder       int    `json:"sort_order"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Store) SaveServiceGroup(ctx context.Context, actor, id int64, in ServiceGroupInput) (int64, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || !contentTextValid(in.Name, 255, false) || in.SortOrder < 0 || in.SortOrder > 1000000 || (in.Icon != "server" && in.Icon != "cloud" && in.Icon != "database" && in.Icon != "globe" && in.Icon != "box") {
		return 0, conflict("invalid_service_group", "服务分组名称、图标或排序无效")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if id != 0 {
		if _, err = LockVersion(ctx, tx, "service_navigation_groups", id, in.ExpectedVersion, "服务分组已变更，请重新加载"); err != nil {
			return 0, err
		}
	}
	action := "service_group.created"
	if id == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM service_navigation_groups").Scan(&count); err != nil {
			return 0, err
		}
		if count >= 100 {
			return 0, conflict("service_group_limit", "最多设置 100 个服务分组")
		}
		err = tx.QueryRow(ctx, "INSERT INTO service_navigation_groups(name,icon,sort_order) VALUES($1,$2,$3) RETURNING id", in.Name, in.Icon, in.SortOrder).Scan(&id)
	} else {
		action = "service_group.updated"
		_, err = UpdateVersioned(ctx, tx, "service_navigation_groups", `name=$1,icon=$2,sort_order=$3`, id, in.ExpectedVersion, in.Name, in.Icon, in.SortOrder)
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actor, action, "service_group", id, in); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) AssignProductServiceGroup(ctx context.Context, actor, product, revision int64, group *int64) error {
	if group != nil && *group < 1 {
		return conflict("invalid_service_group", "服务分组无效")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	if err = tx.QueryRow(ctx, "SELECT revision FROM products WHERE id=$1 FOR UPDATE", product).Scan(&current); err != nil {
		return err
	}
	if current != revision {
		return conflict("version_conflict", "商品已变更，请重新加载")
	}
	if group != nil {
		var id int64
		if err = tx.QueryRow(ctx, "SELECT id FROM service_navigation_groups WHERE id=$1", *group).Scan(&id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE products SET service_group_id=$2,revision=revision+1,updated_at=now() WHERE id=$1", product, group); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actor, "product.service_group.updated", "product", product, map[string]any{"group_id": group, "revision": current + 1}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteServiceGroup(ctx context.Context, actor, id, version int64, target *int64) error {
	if target != nil && (*target < 1 || *target == id) {
		return conflict("invalid_service_group", "转移目标必须是其他服务分组")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = LockVersion(ctx, tx, "service_navigation_groups", id, version, "服务分组已变更，请重新加载"); err != nil {
		return err
	}
	if target != nil {
		var found int64
		if err = tx.QueryRow(ctx, "SELECT id FROM service_navigation_groups WHERE id=$1", *target).Scan(&found); err != nil {
			return err
		}
	}
	result, err := tx.Exec(ctx, "UPDATE products SET service_group_id=$2,revision=revision+1,updated_at=now() WHERE service_group_id=$1", id, target)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM service_navigation_groups WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actor, "service_group.deleted", "service_group", id, map[string]any{"target_group_id": target, "products_moved": result.RowsAffected()}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Call after CatalogReadLock and before touching preferences. The user row here
// serializes first initialization, explicit preference changes and checkout reveal.
func InitializeServiceNavigation(ctx context.Context, tx pgx.Tx, user int64) error {
	result, err := tx.Exec(ctx, "INSERT INTO service_navigation_state(user_id) VALUES($1) ON CONFLICT DO NOTHING", user)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT user_id FROM service_navigation_state WHERE user_id=$1 FOR UPDATE", user); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO service_navigation_preferences(user_id,group_id,visible)
 SELECT $1,id,$2 FROM service_navigation_groups ON CONFLICT DO NOTHING`, user, result.RowsAffected() == 1)
	return err
}

func (s *Store) SetServiceNavigation(ctx context.Context, user, group, version int64, visible bool) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		if err = CatalogReadLock(ctx, tx); err != nil {
			return err
		}
		if err = InitializeServiceNavigation(ctx, tx, user); err != nil {
			return err
		}
		var current int64
		if err = tx.QueryRow(ctx, "SELECT version FROM service_navigation_preferences WHERE user_id=$1 AND group_id=$2 FOR UPDATE", user, group).Scan(&current); err != nil {
			return err
		}
		if current != version {
			return conflict("version_conflict", "导航显示设置已变更，请重新加载")
		}
		if _, err = tx.Exec(ctx, "UPDATE service_navigation_preferences SET visible=$3,version=version+1 WHERE user_id=$1 AND group_id=$2", user, group, visible); err != nil {
			return err
		}
		if err = Audit(ctx, tx, user, "service_navigation.updated", "service_group", group, map[string]any{"visible": visible, "version": current + 1}); err != nil {
			return err
		}
		return nil
	})
}
