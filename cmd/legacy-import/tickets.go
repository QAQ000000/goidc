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

func (im *importer) targetSource(entity string) string {
	if entity == "staff" {
		return im.sourceName + ":staff"
	}
	return im.sourceName
}
func (im *importer) recordTicketMapped(ctx context.Context, tx pgx.Tx, entity string, r legacy.Record, id, runID int64) error {
	if err := im.recordMapped(ctx, tx, entity, r, id, runID); err != nil {
		return err
	}
	if err := im.syncTicketRatingFacts(ctx, tx, entity, r, id); err != nil {
		return err
	}
	return store.Audit(ctx, tx, im.fileAuthorID, "legacy."+entity+".imported", entity, id, map[string]any{"source": im.sourceName, "legacy_id": sourceID(r), "run_id": runID})
}
func optionalTicketRef(r legacy.Record, key string) (int64, error) {
	if r[key] == "" {
		return 0, nil
	}
	return downloadRef(r, key)
}
func ticketState(raw string) string {
	switch raw {
	case "1", "3":
		return "open"
	case "2", "5":
		return "pending"
	case "4":
		return "closed"
	}
	return ""
}
func ticketPriority(raw string) string {
	switch strings.ToLower(raw) {
	case "medium", "normal":
		return "normal"
	case "low":
		return "low"
	case "high":
		return "high"
	case "urgent":
		return "urgent"
	}
	return ""
}
func (im *importer) noteStaff(r legacy.Record) (int64, error) {
	var id int64
	for _, s := range im.source["staff"] {
		if s["user_login"] == r["admin"] {
			if id != 0 {
				return 0, fmt.Errorf("ambiguous ticket note author: %d", sourceID(r))
			}
			id = sourceID(s)
		}
	}
	if id == 0 {
		return 0, fmt.Errorf("missing ticket note author: %d", sourceID(r))
	}
	return id, nil
}
func (im *importer) validateTickets() error {
	for _, entity := range []string{"staff", "ticket_departments", "ticket_statuses", "tickets", "ticket_replies", "ticket_notes", "ticket_attachments"} {
		if _, ok := im.source[entity]; !ok {
			return fmt.Errorf("ticket snapshot required: %s", entity)
		}
	}
	staff, departments, statuses, tickets, users, services := index(im.source["staff"]), index(im.source["ticket_departments"]), index(im.source["ticket_statuses"]), index(im.source["tickets"]), index(im.source["users"]), index(im.source["services"])
	names := map[string]bool{}
	for _, r := range im.source["staff"] {
		name := r["user_login"]
		if name == "" || names[name] || !legacy.DownloadTextValid(name, 255, false) || !legacy.DownloadTextValid(r["user_nickname"], 255, false) || (r["user_status"] != "0" && r["user_status"] != "1" && r["user_status"] != "2") {
			return fmt.Errorf("invalid historical staff: %d", sourceID(r))
		}
		names[name] = true
	}
	for _, r := range im.source["ticket_departments"] {
		if strings.TrimSpace(r["name"]) == "" || !legacy.DownloadTextValid(r["name"], 255, false) || !legacy.DownloadTextValid(r["description"], 100000, true) {
			return fmt.Errorf("invalid ticket department: %d", sourceID(r))
		}
		order, err := strconv.ParseInt(r["order"], 10, 64)
		if err != nil || order < -100000 || order > 100000 {
			return fmt.Errorf("invalid department sort: %d", sourceID(r))
		}
		for _, key := range []string{"hidden", "only_reg_client", "only_client_open", "no_auto_reply", "is_product_order", "is_related_upstream", "is_certifi"} {
			if r[key] != "0" && r[key] != "1" {
				return fmt.Errorf("invalid ticket department flag: %d/%s", sourceID(r), key)
			}
		}
		// Registered-customer tickets need no guest access. Unsupported eligibility
		// policies cannot become unrestricted target departments on migration.
		if r["only_client_open"] != "0" || r["is_product_order"] != "0" || r["is_certifi"] != "0" {
			return fmt.Errorf("unsupported ticket department eligibility policy: %d", sourceID(r))
		}
	}
	for _, r := range im.source["ticket_statuses"] {
		if strings.TrimSpace(r["title"]) == "" || !legacy.DownloadTextValid(r["title"], 64, false) {
			return fmt.Errorf("invalid legacy ticket status: %d", sourceID(r))
		}
		for _, key := range []string{"show_active", "show_await", "auto_close"} {
			if r[key] != "0" && r[key] != "1" {
				return fmt.Errorf("invalid legacy ticket status flag: %d", sourceID(r))
			}
		}
	}
	for _, r := range im.source["tickets"] {
		id := sourceID(r)
		uid, err := downloadRef(r, "uid")
		if err != nil {
			return err
		}
		if uid == 0 || users[uid] == nil {
			return fmt.Errorf("registered ticket owner required: %d", id)
		}
		for _, key := range []string{"dptid", "host_id", "merged_ticket_id", "handle", "admin_id", "status"} {
			if _, err = downloadRef(r, key); err != nil {
				return err
			}
		}
		if d := referenceID(r, "dptid"); d != 0 && departments[d] == nil {
			return fmt.Errorf("missing ticket department: %d", id)
		}
		if h := referenceID(r, "host_id"); h != 0 && (services[h] == nil || services[h]["uid"] != r["uid"]) {
			return fmt.Errorf("ticket service owner mismatch: %d", id)
		}
		for _, key := range []string{"handle", "admin_id"} {
			if a := referenceID(r, key); a != 0 && staff[a] == nil {
				return fmt.Errorf("missing ticket historical staff: %d/%s", id, key)
			}
		}
		if statuses[referenceID(r, "status")] == nil || ticketState(r["status"]) == "" {
			return fmt.Errorf("unsupported legacy ticket state: %d", id)
		}
		if ticketPriority(r["priority"]) == "" || strings.TrimSpace(r["title"]) == "" || !legacy.DownloadTextValid(r["title"], 255, false) || !legacy.DownloadTextValid(r["content"], 100000, true) || !legacy.DownloadTextValid(r["tid"], 128, false) {
			return fmt.Errorf("invalid ticket text or priority: %d", id)
		}
		seen := map[int64]bool{id: true}
		parent := referenceID(r, "merged_ticket_id")
		for parent > 0 {
			if seen[parent] || tickets[parent] == nil || tickets[parent]["uid"] != r["uid"] {
				return fmt.Errorf("invalid ticket merge lineage: %d", id)
			}
			seen[parent] = true
			parent = referenceID(tickets[parent], "merged_ticket_id")
		}
	}
	for _, entity := range []string{"ticket_replies", "ticket_notes"} {
		for _, r := range im.source[entity] {
			id := sourceID(r)
			tid, err := downloadRef(r, "tid")
			if err != nil {
				return err
			}
			parent := tickets[tid]
			if parent == nil {
				return fmt.Errorf("missing ticket parent: %s/%d", entity, id)
			}
			if !legacy.DownloadTextValid(r["content"], 100000, true) || (strings.TrimSpace(r["content"]) == "" && r["attachment"] == "") || (r["editor"] != "plain" && r["editor"] != "markdown") {
				return fmt.Errorf("invalid ticket message: %s/%d", entity, id)
			}
			if entity == "ticket_notes" {
				if _, err = im.noteStaff(r); err != nil {
					return err
				}
			} else {
				uid, err := downloadRef(r, "uid")
				if err != nil {
					return err
				}
				admin, err := optionalTicketRef(r, "admin_id")
				if err != nil {
					return err
				}
				contact, err := downloadRef(r, "contactid")
				if err != nil {
					return err
				}
				if contact != 0 {
					return fmt.Errorf("contact ticket identity migration required: %d", id)
				}
				if admin > 0 {
					if staff[admin] == nil || (uid != 0 && uid != referenceID(parent, "uid")) {
						return fmt.Errorf("invalid staff ticket reply owner: %d", id)
					}
				} else if uid != referenceID(parent, "uid") {
					return fmt.Errorf("ticket reply owner mismatch: %d", id)
				}
			}
		}
	}
	for _, entity := range []string{"staff", "tickets", "ticket_replies", "ticket_notes"} {
		for _, r := range im.source[entity] {
			keys := []string{"create_time"}
			if entity == "tickets" {
				keys = append(keys, "update_time", "last_reply_time", "handle_time")
			}
			for _, key := range keys {
				n, err := downloadRef(r, key)
				if err != nil || n > 253402300799 {
					return fmt.Errorf("invalid ticket timestamp: %s/%d/%s", entity, sourceID(r), key)
				}
			}
		}
	}
	return nil
}
func (im *importer) prepareTicketFiles() error {
	im.report.Counts["ticket_attachments"] = len(im.source["ticket_attachments"])
	for _, r := range im.source["ticket_attachments"] {
		b, mt, err := legacy.ReadTicketFile(im.uploadRoot, r["stored_name"], r["filename"])
		if err != nil {
			return fmt.Errorf("legacy ticket attachment %d: %s", sourceID(r), err)
		}
		r["_file_sha256"] = checksum(b)
		r["_file_size"] = strconv.Itoa(len(b))
		r["_file_mime"] = mt
	}
	return nil
}
func (im *importer) canonicalTicket(id int64) int64 {
	records := index(im.source["tickets"])
	for referenceID(records[id], "merged_ticket_id") > 0 {
		id = referenceID(records[id], "merged_ticket_id")
	}
	return id
}
func (im *importer) importTickets(ctx context.Context, tx pgx.Tx, users, services map[int64]int64, runID int64) error {
	staff, departments, tickets, replies, notes, attachments := map[int64]int64{}, map[int64]int64{}, map[int64]int64{}, map[int64]int64{}, map[int64]int64{}, map[int64]int64{}
	var role int64
	for _, r := range im.source["staff"] {
		replay, err := im.replay(ctx, tx, "staff", r, staff)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		if err = im.fileTargetIsNew(ctx, tx, "staff", r); err != nil {
			return err
		}
		if role == 0 {
			name := "迁移历史作者-" + checksum([]byte(im.sourceName))[:12]
			if _, err = tx.Exec(ctx, `INSERT INTO admin_roles(name,description,active,permissions) VALUES($1,'仅保留历史作者身份；启用和权限必须另行复核',false,'{}') ON CONFLICT(name) DO NOTHING`, name); err != nil {
				return err
			}
			var safe bool
			if err = tx.QueryRow(ctx, "SELECT id,NOT active AND cardinality(permissions)=0 FROM admin_roles WHERE name=$1", name).Scan(&role, &safe); err != nil {
				return err
			}
			if !safe {
				return fmt.Errorf("historical staff role requires empty disabled permissions")
			}
		}
		name := r["user_nickname"]
		if name == "" {
			name = r["user_login"]
		}
		hash, err := randomHash()
		if err != nil {
			return err
		}
		username := "legacy-staff-" + checksum([]byte(im.sourceName))[:8] + "-" + r["id"]
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role,admin_role_id,status,password_reset_required,legacy_source,legacy_id,created_at,updated_at)
 VALUES($1,$2,$3,$4,'admin',$5,'disabled',true,$6,$7,$8,$8) RETURNING id`, username, username+"@import.invalid", hash, name, role, im.targetSource("staff"), sourceID(r), ts(r["create_time"])).Scan(&id); err != nil {
			return err
		}
		staff[sourceID(r)] = id
		im.report.Mapped["staff"]++
		if err = im.recordTicketMapped(ctx, tx, "staff", r, id, runID); err != nil {
			return err
		}
	}
	for _, r := range im.source["ticket_departments"] {
		replay, err := im.replay(ctx, tx, "ticket_departments", r, departments)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		if err = im.fileTargetIsNew(ctx, tx, "ticket_departments", r); err != nil {
			return err
		}
		var id int64
		slug := "legacy-dept-" + checksum([]byte(im.sourceName))[:8] + "-" + r["id"]
		if err = tx.QueryRow(ctx, `INSERT INTO ticket_departments(name,slug,sort_order,active,customer_visible,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$4,$5,$6) RETURNING id`, strings.TrimSpace(r["name"]), slug, referenceID(r, "order"), r["hidden"] == "0", im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return err
		}
		departments[sourceID(r)] = id
		im.report.Mapped["ticket_departments"]++
		if err = im.recordTicketMapped(ctx, tx, "ticket_departments", r, id, runID); err != nil {
			return err
		}
	}
	newlyCreated := map[int64]bool{}
	statuses := index(im.source["ticket_statuses"])
	for _, r := range im.source["tickets"] {
		replay, err := im.replay(ctx, tx, "tickets", r, tickets)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		if err = im.fileTargetIsNew(ctx, tx, "tickets", r); err != nil {
			return err
		}
		var department, assignee, service *int64
		if d := referenceID(r, "dptid"); d > 0 {
			n := departments[d]
			department = &n
		}
		if a := referenceID(r, "handle"); a > 0 {
			n := staff[a]
			assignee = &n
		}
		if s := referenceID(r, "host_id"); s > 0 {
			n := services[s]
			if n == 0 {
				return fmt.Errorf("unmapped ticket service: %d", sourceID(r))
			}
			service = &n
		}
		// A source ticket number is not guaranteed unique. Keep the original label
		// separately and use a stable namespace plus source ID for the target number.
		no := "LEGACY-" + checksum([]byte(im.sourceName))[:8] + "-" + r["id"]
		state := ticketState(r["status"])
		if referenceID(r, "merged_ticket_id") > 0 {
			state = "closed"
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO tickets(ticket_no,user_id,department_id,assignee_id,service_id,subject,body,status,priority,created_at,updated_at,legacy_status_label,legacy_source,legacy_id,legacy_ticket_no)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING id`, no, users[referenceID(r, "uid")], department, assignee, service, r["title"], r["content"], state, ticketPriority(r["priority"]), ts(r["create_time"]), sourceUpdated(r), statuses[referenceID(r, "status")]["title"], im.sourceName, sourceID(r), r["tid"]).Scan(&id); err != nil {
			return err
		}
		tickets[sourceID(r)] = id
		newlyCreated[sourceID(r)] = true
		im.report.Mapped["tickets"]++
		if err = im.recordTicketMapped(ctx, tx, "tickets", r, id, runID); err != nil {
			return err
		}
	}
	for _, r := range im.source["tickets"] {
		if newlyCreated[sourceID(r)] && referenceID(r, "merged_ticket_id") > 0 {
			if _, err := tx.Exec(ctx, "UPDATE tickets SET merged_ticket_id=$2 WHERE id=$1", tickets[sourceID(r)], tickets[im.canonicalTicket(sourceID(r))]); err != nil {
				return err
			}
		}
	}
	for _, entity := range []string{"ticket_replies", "ticket_notes"} {
		out := replies
		if entity == "ticket_notes" {
			out = notes
		}
		for _, r := range im.source[entity] {
			replay, err := im.replay(ctx, tx, entity, r, out)
			if err != nil {
				return err
			}
			if replay {
				continue
			}
			if err = im.fileTargetIsNew(ctx, tx, entity, r); err != nil {
				return err
			}
			author := users[referenceID(r, "uid")]
			if entity == "ticket_notes" {
				old, err := im.noteStaff(r)
				if err != nil {
					return err
				}
				author = staff[old]
			} else if a := referenceID(r, "admin_id"); a > 0 {
				author = staff[a]
			}
			var id int64
			if err = tx.QueryRow(ctx, "INSERT INTO "+entity+"(ticket_id,author_id,body,created_at,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id", tickets[im.canonicalTicket(referenceID(r, "tid"))], author, r["content"], ts(r["create_time"]), im.sourceName, sourceID(r)).Scan(&id); err != nil {
				return err
			}
			out[sourceID(r)] = id
			im.report.Mapped[entity]++
			if err = im.recordTicketMapped(ctx, tx, entity, r, id, runID); err != nil {
				return err
			}
		}
	}
	for _, r := range im.source["ticket_attachments"] {
		replay, err := im.replay(ctx, tx, "ticket_attachments", r, attachments)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		if err = im.fileTargetIsNew(ctx, tx, "ticket_attachments", r); err != nil {
			return err
		}
		b, mt, err := legacy.ReadTicketFile(im.uploadRoot, r["stored_name"], r["filename"])
		if err != nil {
			return fmt.Errorf("legacy ticket attachment %d: %s", sourceID(r), err)
		}
		hash := checksum(b)
		if hash != r["_file_sha256"] {
			return fmt.Errorf("legacy ticket bytes changed after preflight: %d", sourceID(r))
		}
		parent := index(im.source[r["parent_entity"]])[referenceID(r, "parent_id")]
		ticket := index(im.source["tickets"])[referenceID(r, "ticket_id")]
		author := users[referenceID(ticket, "uid")]
		var reply, note *int64
		if r["parent_entity"] == "tickets" {
			if a := referenceID(parent, "admin_id"); a > 0 {
				author = staff[a]
			}
		} else if r["parent_entity"] == "ticket_replies" {
			n := replies[referenceID(r, "parent_id")]
			reply = &n
			if a := referenceID(parent, "admin_id"); a > 0 {
				author = staff[a]
			}
		} else {
			n := notes[referenceID(r, "parent_id")]
			note = &n
			a, err := im.noteStaff(parent)
			if err != nil {
				return err
			}
			author = staff[a]
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO ticket_attachments(ticket_id,uploader_id,reply_id,note_id,filename,mime_type,size_bytes,sha256,content,created_at,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`, tickets[im.canonicalTicket(referenceID(r, "ticket_id"))], author, reply, note, r["filename"], mt, len(b), hash, b, ts(r["create_time"]), im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return err
		}
		attachments[sourceID(r)] = id
		im.report.Mapped["ticket_attachments"]++
		if err = im.recordTicketMapped(ctx, tx, "ticket_attachments", r, id, runID); err != nil {
			return err
		}
	}
	return nil
}

// The source has a per-reply star but no rating time. Keep that distinction.
func (im *importer) syncTicketRatingFacts(ctx context.Context, tx pgx.Tx, entity string, r legacy.Record, id int64) error {
	if entity == "tickets" {
		if admin := referenceID(r, "admin_id"); admin > 0 {
			_, err := tx.Exec(ctx, `UPDATE tickets SET created_by=(SELECT target_id FROM legacy_id_map WHERE source=$2||':staff' AND entity='staff' AND legacy_id=$3),initial_author_kind='staff' WHERE id=$1`, id, im.sourceName, admin)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE tickets SET initial_author_kind='customer' WHERE id=$1`, id)
		return err
	}
	if entity != "ticket_replies" {
		return nil
	}
	kind := "customer"
	if referenceID(r, "admin_id") > 0 {
		kind = "staff"
	}
	if _, err := tx.Exec(ctx, `UPDATE ticket_replies SET author_kind=$2 WHERE id=$1`, id, kind); err != nil {
		return err
	}
	if kind == "staff" && len(r["star"]) == 1 && r["star"] >= "1" && r["star"] <= "5" {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_ratings(reply_id,customer_id,staff_id,stars,provenance) SELECT r.id,t.user_id,r.author_id,$2,'legacy' FROM ticket_replies r JOIN tickets t ON t.id=r.ticket_id WHERE r.id=$1 ON CONFLICT(reply_id) DO NOTHING`, id, int(r["star"][0]-'0'))
		return err
	}
	return nil
}
