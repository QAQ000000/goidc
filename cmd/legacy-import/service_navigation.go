package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/jackc/pgx/v5"
)

func referenceID(r legacy.Record, key string) int64 {
	n, _ := strconv.ParseInt(r[key], 10, 64)
	return n
}
func navigationReference(r legacy.Record, key string) (int64, error) {
	if r[key] == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(r[key], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid navigation reference: %d/%s", sourceID(r), key)
	}
	return n, nil
}
func (im *importer) validateServiceNavigation() error {
	for _, entity := range []string{"service_navigation_groups", "service_navigation_preferences"} {
		if _, ok := im.source[entity]; !ok {
			return fmt.Errorf("navigation snapshot required: %s", entity)
		}
	}
	for _, r := range im.source["service_navigation_groups"] {
		name := strings.TrimSpace(r["groupname"])
		if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 255 {
			return fmt.Errorf("invalid navigation name: %d", sourceID(r))
		}
		for _, c := range name {
			if unicode.IsControl(c) {
				return fmt.Errorf("invalid navigation name: %d", sourceID(r))
			}
		}
		order, err := strconv.ParseInt(r["order"], 10, 64)
		if err != nil || order < 0 || order > 1000000 {
			return fmt.Errorf("invalid navigation sort: %d", sourceID(r))
		}
	}
	for _, r := range im.source["products"] {
		if _, err := navigationReference(r, "groupid"); err != nil {
			return err
		}
	}
	pairs := map[[2]int64]int64{}
	for _, r := range im.source["service_navigation_preferences"] {
		uid, err := navigationReference(r, "uid")
		if err != nil {
			return err
		}
		gid, err := navigationReference(r, "groupid")
		if err != nil {
			return err
		}
		if r["is_show"] != "" && r["is_show"] != "0" && r["is_show"] != "1" {
			return fmt.Errorf("invalid navigation visibility: %d", sourceID(r))
		}
		pair := [2]int64{uid, gid}
		if previous, ok := pairs[pair]; ok {
			return fmt.Errorf("duplicate navigation preference: %d/%d; source review required", previous, sourceID(r))
		}
		pairs[pair] = sourceID(r)
	}
	return nil
}

// Only recognized icon classes are mapped. Raw source classes remain archived;
// no CSS class or HTML from MySQL is used in the web interface.

func (im *importer) navigationRecordIsNew(ctx context.Context, tx pgx.Tx, entity string, r legacy.Record) error {
	var archived bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1 AND entity=$2 AND legacy_id=$3)", im.sourceName, entity, sourceID(r)).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return fmt.Errorf("missing archived navigation target: %s/%d; restore or review required", entity, sourceID(r))
	}
	return nil
}

func (im *importer) importServiceGroups(ctx context.Context, tx pgx.Tx, out map[int64]int64, runID int64) error {
	for _, r := range im.source["service_navigation_groups"] {
		if replayed, err := im.replay(ctx, tx, "service_navigation_groups", r, out); err != nil {
			return err
		} else if replayed {
			continue
		}
		if err := im.navigationRecordIsNew(ctx, tx, "service_navigation_groups", r); err != nil {
			return err
		}
		icon, _ := legacy.ServiceNavigationIcon(r["fa_icon"])
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO service_navigation_groups(name,icon,sort_order,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5) RETURNING id`, strings.TrimSpace(r["groupname"]), icon, referenceID(r, "order"), im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return err
		}
		out[sourceID(r)] = id
		im.report.Mapped["service_navigation_groups"]++
		if err := im.recordMapped(ctx, tx, "service_navigation_groups", r, id, runID); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) importNavigationPreferences(ctx context.Context, tx pgx.Tx, users, groups map[int64]int64, runID int64) error {
	// Any original preference row means updateUgp considered this user initialized,
	// including a row for a deleted group. Missing source groups therefore stay hidden.
	initialized := map[int64]bool{}
	for _, r := range im.source["service_navigation_preferences"] {
		initialized[referenceID(r, "uid")] = true
	}
	groupIDs := make([]int64, 0, len(groups))
	for _, id := range groups {
		groupIDs = append(groupIDs, id)
	}
	for _, r := range im.source["users"] {
		old := sourceID(r)
		if !initialized[old] {
			continue
		}
		user := users[old]
		var archived, state bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1 AND entity='service_navigation_preferences' AND nullif(payload->>'uid','')::bigint=$2),EXISTS(SELECT 1 FROM service_navigation_state WHERE user_id=$3)`, im.sourceName, old, user).Scan(&archived, &state); err != nil {
			return err
		}
		if archived {
			if !state {
				return fmt.Errorf("missing archived navigation initialization: users/%d", old)
			}
			continue
		}
		if state {
			return fmt.Errorf("unverified existing navigation initialization: users/%d", old)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO service_navigation_state(user_id) VALUES($1)", user); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO service_navigation_preferences(user_id,group_id,visible) SELECT $1,id,false FROM service_navigation_groups WHERE id=ANY($2::bigint[])`, user, groupIDs); err != nil {
			return err
		}
	}
	out := map[int64]int64{}
	for _, r := range im.source["service_navigation_preferences"] {
		user, group := users[referenceID(r, "uid")], groups[referenceID(r, "groupid")]
		if user == 0 || group == 0 {
			continue
		} // Explicit issues plus full source archive preserve orphan evidence.
		if replayed, err := im.replay(ctx, tx, "service_navigation_preferences", r, out); err != nil {
			return err
		} else if replayed {
			var owned bool
			if err = tx.QueryRow(ctx, "SELECT user_id=$2 AND group_id=$3 FROM service_navigation_preferences WHERE id=$1", out[sourceID(r)], user, group).Scan(&owned); err != nil {
				return err
			}
			if !owned {
				return fmt.Errorf("navigation target owner mismatch: %d", sourceID(r))
			}
			continue
		}
		if err := im.navigationRecordIsNew(ctx, tx, "service_navigation_preferences", r); err != nil {
			return err
		}
		var target int64
		if err := tx.QueryRow(ctx, `INSERT INTO service_navigation_preferences(user_id,group_id,visible,legacy_source,legacy_id)
  VALUES($1,$2,$3,$4,$5) ON CONFLICT(user_id,group_id) DO UPDATE SET visible=EXCLUDED.visible,legacy_source=EXCLUDED.legacy_source,legacy_id=EXCLUDED.legacy_id
  WHERE service_navigation_preferences.legacy_source='' AND service_navigation_preferences.legacy_id IS NULL AND service_navigation_preferences.version=1 RETURNING id`, user, group, r["is_show"] == "1", im.sourceName, sourceID(r)).Scan(&target); err != nil {
			return err
		}
		im.report.Mapped["service_navigation_preferences"]++
		if err := im.recordMapped(ctx, tx, "service_navigation_preferences", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}
