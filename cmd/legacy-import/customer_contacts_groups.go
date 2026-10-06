package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func parseLegacyInt(raw string, field string, id int64) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %d", field, id)
	}
	return n, nil
}

func legacyFlag(raw string, field string, id int64) (bool, error) {
	if raw == "" {
		return false, nil
	}
	if raw != "0" && raw != "1" {
		return false, fmt.Errorf("invalid %s: %d", field, id)
	}
	return raw == "1", nil
}

func validLegacyColor(raw string) bool {
	if len(raw) != 7 || raw[0] != '#' {
		return false
	}
	for _, c := range raw[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (im *importer) validateCustomerContactsGroups() error {
	groupNames := map[string]int64{}
	for _, r := range im.source["customer_groups"] {
		id := sourceID(r)
		name := strings.TrimSpace(r["group_name"])
		if name == "" || !legacy.DownloadTextValid(name, 45, false) {
			return fmt.Errorf("invalid customer group name: %d", id)
		}
		key := strings.ToLower(name)
		if previous, exists := groupNames[key]; exists {
			return fmt.Errorf("duplicate customer group name: %d/%d", previous, id)
		}
		groupNames[key] = id
		if color := strings.TrimSpace(r["group_colour"]); color != "" && !validLegacyColor(color) {
			return fmt.Errorf("invalid customer group color: %d", id)
		}
		discount, err := parseLegacyInt(r["discount_percent"], "customer group discount", id)
		if err != nil || discount < 0 || discount > 100 {
			return fmt.Errorf("invalid customer group discount: %d", id)
		}
		if _, err = legacyFlag(r["susptermexempt"], "customer group suspension flag", id); err != nil {
			return err
		}
		if _, err = legacyFlag(r["separateinvoices"], "customer group invoice flag", id); err != nil {
			return err
		}
	}
	contactEmails := map[string]int64{}
	for _, r := range im.source["contacts"] {
		id := sourceID(r)
		uid, err := parseLegacyInt(r["uid"], "contact owner", id)
		if err != nil || uid < 1 {
			return fmt.Errorf("invalid contact owner: %d", id)
		}
		sex, err := parseLegacyInt(r["sex"], "contact sex", id)
		if err != nil || sex < 0 || sex > 2 {
			return fmt.Errorf("invalid contact sex: %d", id)
		}
		status := r["status"]
		if status != "0" && status != "1" && status != "2" {
			return fmt.Errorf("invalid contact status: %d", id)
		}
		for _, field := range []struct {
			name string
			max  int
		}{
			{"username", 50}, {"avatar", 255}, {"companyname", 80}, {"email", 100}, {"country", 100}, {"province", 100}, {"city", 100}, {"region", 100}, {"address1", 100}, {"address2", 100}, {"postcode", 100}, {"phonenumber", 100}, {"permissions", 10000},
		} {
			if !legacy.DownloadTextValid(r[field.name], field.max, false) {
				return fmt.Errorf("invalid contact text: %d/%s", id, field.name)
			}
		}
		for _, field := range []string{"generalemails", "invoiceemails", "productemails", "supportemails"} {
			if _, err = legacyFlag(r[field], "contact notification flag", id); err != nil {
				return err
			}
		}
		email := strings.ToLower(strings.TrimSpace(r["email"]))
		if email != "" && validEmail(email, id) == email {
			if previous, exists := contactEmails[email]; exists {
				return fmt.Errorf("duplicate contact email: %d/%d", previous, id)
			}
			contactEmails[email] = id
		}
		for _, field := range []string{"create_time", "update_time"} {
			n, parseErr := parseLegacyInt(r[field], "contact timestamp", id)
			if parseErr != nil || n < 0 || n > 253402300799 {
				return fmt.Errorf("invalid contact timestamp: %d/%s", id, field)
			}
		}
	}
	return nil
}

func (im *importer) importCustomerGroups(ctx context.Context, tx pgx.Tx, out map[int64]int64, runID int64) error {
	for _, r := range im.source["customer_groups"] {
		id := sourceID(r)
		if replayed, err := im.replay(ctx, tx, "customer_groups", r, out); err != nil {
			return err
		} else if replayed {
			continue
		}
		color := strings.TrimSpace(r["group_colour"])
		if color == "" {
			color = "#64748b"
		}
		discount, _ := parseLegacyInt(r["discount_percent"], "customer group discount", id)
		suspension, _ := legacyFlag(r["susptermexempt"], "customer group suspension flag", id)
		separate, _ := legacyFlag(r["separateinvoices"], "customer group invoice flag", id)
		var target int64
		if err := tx.QueryRow(ctx, `INSERT INTO customer_groups(name,color,discount_percent,suspend_delete_exempt,separate_invoices,legacy_source,legacy_id)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, strings.TrimSpace(r["group_name"]), color, discount, suspension, separate, im.sourceName, id).Scan(&target); err != nil {
			return err
		}
		out[id] = target
		im.report.Mapped["customer_groups"]++
		if err := im.recordMapped(ctx, tx, "customer_groups", r, target, runID); err != nil {
			return err
		}
		if err := store.Audit(ctx, tx, im.fileAuthorID, "legacy.customer_group.imported", "customer_group", target, map[string]any{"source": im.sourceName, "legacy_id": id, "run_id": runID}); err != nil {
			return err
		}
	}
	return nil
}

func legacyContactPermissions(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var values []string
	if strings.HasPrefix(raw, "[") {
		if json.Unmarshal([]byte(raw), &values) == nil {
			raw = ""
		}
	}
	if raw != "" {
		values = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func legacyContactStatus(raw string) string {
	switch raw {
	case "1":
		return "active"
	case "2":
		return "disabled"
	default:
		return "inactive"
	}
}

func (im *importer) importContacts(ctx context.Context, tx pgx.Tx, users map[int64]int64, runID int64) error {
	for _, r := range im.source["contacts"] {
		id := sourceID(r)
		if replayed, err := im.replay(ctx, tx, "contacts", r, map[int64]int64{}); err != nil {
			return err
		} else if replayed {
			continue
		}
		owner := users[referenceID(r, "uid")]
		if owner == 0 {
			im.report.Issues = append(im.report.Issues, legacy.Issue{Entity: "contacts", LegacyID: id, Code: "missing_customer", Detail: map[string]any{"uid": referenceID(r, "uid")}})
			continue
		}
		email := strings.TrimSpace(r["email"])
		if validEmail(email, id) != strings.ToLower(email) {
			im.report.Issues = append(im.report.Issues, legacy.Issue{Entity: "contacts", LegacyID: id, Code: "invalid_email_placeholder", Detail: map[string]any{"legacy_email": email}})
		}
		sex, _ := parseLegacyInt(r["sex"], "contact sex", id)
		general, _ := legacyFlag(r["generalemails"], "contact notification flag", id)
		invoice, _ := legacyFlag(r["invoiceemails"], "contact notification flag", id)
		product, _ := legacyFlag(r["productemails"], "contact notification flag", id)
		support, _ := legacyFlag(r["supportemails"], "contact notification flag", id)
		wechat := strings.TrimSpace(r["wechat_id"])
		var target int64
		if err := tx.QueryRow(ctx, `INSERT INTO customer_contacts(customer_id,username,sex,avatar,company_name,email,wechat_id,country,province,city,region,address1,address2,postcode,phone_number,permissions,general_emails,invoice_emails,product_emails,support_emails,status,created_at,updated_at,legacy_source,legacy_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25) RETURNING id`, owner, strings.TrimSpace(r["username"]), sex, strings.TrimSpace(r["avatar"]), strings.TrimSpace(r["companyname"]), validEmail(email, id), wechat, strings.TrimSpace(r["country"]), strings.TrimSpace(r["province"]), strings.TrimSpace(r["city"]), strings.TrimSpace(r["region"]), strings.TrimSpace(r["address1"]), strings.TrimSpace(r["address2"]), strings.TrimSpace(r["postcode"]), strings.TrimSpace(r["phonenumber"]), legacyContactPermissions(r["permissions"]), general, invoice, product, support, legacyContactStatus(r["status"]), ts(r["create_time"]), ts(r["update_time"]), im.sourceName, id).Scan(&target); err != nil {
			return err
		}
		im.report.Mapped["contacts"]++
		if err := im.recordMapped(ctx, tx, "contacts", r, target, runID); err != nil {
			return err
		}
		if err := store.Audit(ctx, tx, im.fileAuthorID, "legacy.contact.imported", "customer_contact", target, map[string]any{"source": im.sourceName, "legacy_id": id, "run_id": runID}); err != nil {
			return err
		}
	}
	return nil
}
