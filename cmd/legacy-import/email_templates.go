package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func emailTemplateKind(id int64) string { return "legacy_email_" + strconv.FormatInt(id, 10) }

func (im *importer) validateEmailTemplates() error {
	rows, ok := im.source["email_templates"]
	if !ok {
		return nil
	}
	seen := map[int64]bool{}
	for _, r := range rows {
		id := sourceID(r)
		if id < 1 || seen[id] {
			return fmt.Errorf("invalid or duplicate email template: %d", id)
		}
		seen[id] = true
		if strings.TrimSpace(r["type"]) == "" || !legacy.DownloadTextValid(r["type"], 100, false) || strings.TrimSpace(r["name"]) == "" || !legacy.DownloadTextValid(r["name"], 120, false) || strings.TrimSpace(r["subject"]) == "" || !legacy.DownloadTextValid(r["subject"], 240, true) || !legacy.DownloadTextValid(r["message"], 100000, true) {
			return fmt.Errorf("invalid email template text: %d", id)
		}
		if r["disabled"] != "0" && r["disabled"] != "1" {
			return fmt.Errorf("invalid email template state: %d", id)
		}
		for _, field := range []string{"create_time", "update_time"} {
			n, err := downloadRef(r, field)
			if err != nil || n > 253402300799 {
				return fmt.Errorf("invalid email template timestamp: %d/%s", id, field)
			}
		}
	}
	return nil
}

func (im *importer) importEmailTemplates(ctx context.Context, tx pgx.Tx, runID int64) error {
	for _, r := range im.source["email_templates"] {
		id := sourceID(r)
		mapped := map[int64]int64{}
		replayed, err := im.replay(ctx, tx, "email_templates", r, mapped)
		if err != nil {
			return err
		}
		if replayed {
			continue
		}
		body := strings.TrimSpace(r["message"])
		active := r["disabled"] == "0" && body != ""
		if body == "" {
			body = strings.TrimSpace(r["name"])
			im.report.Issues = append(im.report.Issues, legacy.Issue{Entity: "email_templates", LegacyID: id, Code: "empty_body_disabled", Detail: map[string]any{"legacy_type": r["type"]}})
		}
		var target int64
		if err = tx.QueryRow(ctx, `INSERT INTO notification_templates(kind,name,subject,body,active,updated_by,created_at,updated_at,legacy_source,legacy_id,legacy_type,legacy_language)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT (legacy_source,legacy_id) WHERE legacy_source<>'' AND legacy_id IS NOT NULL
DO UPDATE SET kind=EXCLUDED.kind,legacy_type=EXCLUDED.legacy_type,legacy_language=EXCLUDED.legacy_language
RETURNING id`, emailTemplateKind(id), strings.TrimSpace(r["name"]), strings.TrimSpace(r["subject"]), body, active, im.fileAuthorID, ts(r["create_time"]), ts(r["update_time"]), im.sourceName, id, strings.TrimSpace(r["type"]), strings.TrimSpace(r["language"])).Scan(&target); err != nil {
			return err
		}
		if err = store.Audit(ctx, tx, im.fileAuthorID, "legacy.email_template.imported", "notification_template", target, map[string]any{"source": im.sourceName, "legacy_id": id, "legacy_type": r["type"], "run_id": runID}); err != nil {
			return err
		}
		im.report.Mapped["email_templates"]++
		if err = im.recordMapped(ctx, tx, "email_templates", r, target, runID); err != nil {
			return err
		}
	}
	return nil
}
