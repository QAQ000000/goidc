package store

import (
	"context"
	"strings"
)

type ContentTagInput struct {
	ContentType     string `json:"content_type"`
	Name            string `json:"name"`
	Slug            string `json:"slug"`
	SortOrder       int    `json:"sort_order"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Store) SaveContentTag(ctx context.Context, actorID, id int64, in ContentTagInput) (int64, error) {
	in.ContentType = strings.ToLower(strings.TrimSpace(in.ContentType))
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	if !contentTypeValid(in.ContentType) || in.Name == "" || !contentTextValid(in.Name, 120, false) || !contentSlug.MatchString(in.Slug) || in.SortOrder < 0 || in.SortOrder > 1000000 {
		return 0, conflict("invalid_tag", "标签字段无效")
	}
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	action := "content_tag.created"
	var current int64
	if id == 0 {
		err = tx.QueryRow(ctx, "INSERT INTO content_tags(content_type,name,slug,sort_order,active) VALUES($1,$2,$3,$4,$5) RETURNING id", in.ContentType, in.Name, in.Slug, in.SortOrder, in.Active).Scan(&id)
	} else {
		var oldType string
		if err = tx.QueryRow(ctx, "SELECT content_type,version FROM content_tags WHERE id=$1 FOR UPDATE", id).Scan(&oldType, &current); err != nil {
			return 0, err
		}
		if current != in.ExpectedVersion {
			return 0, conflict("version_conflict", "标签已变更，请重新加载")
		}
		if oldType != in.ContentType {
			return 0, conflict("content_type_immutable", "标签类型不能改变")
		}
		action = "content_tag.updated"
		_, err = tx.Exec(ctx, "UPDATE content_tags SET name=$2,slug=$3,sort_order=$4,active=$5,version=version+1,updated_at=now() WHERE id=$1", id, in.Name, in.Slug, in.SortOrder, in.Active)
	}
	if err != nil {
		return 0, err
	}
	if err = Audit(ctx, tx, actorID, action, "content_tag", id, map[string]any{"name": in.Name, "content_type": in.ContentType, "active": in.Active, "sort_order": in.SortOrder, "version": current + 1}); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DeleteContentTag(ctx context.Context, actorID, id, version int64) error {
	tx, err := s.contentTx(ctx, actorID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current int64
	var name string
	if err = tx.QueryRow(ctx, "SELECT version,name FROM content_tags WHERE id=$1 FOR UPDATE", id).Scan(&current, &name); err != nil {
		return err
	}
	if current != version {
		return conflict("version_conflict", "标签已变更，请重新加载")
	}
	var used bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM content_article_tags WHERE tag_id=$1)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return conflict("tag_in_use", "标签仍有关联文章，无法删除")
	}
	if _, err = tx.Exec(ctx, "DELETE FROM content_tags WHERE id=$1", id); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "content_tag.deleted", "content_tag", id, map[string]any{"name": name, "version": version}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
