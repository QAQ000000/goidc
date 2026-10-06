package legacy

import (
	"fmt"
	"strconv"
	"strings"
)

// Attachment IDs are deterministic ordinals of the ordered source parents and
// CSV slots. The immutable snapshot contract rejects additions/removals/reorder.
func ExpandTicketAttachments(s Snapshot) error {
	refs := []Record{}
	var id int64
	for _, entity := range []string{"tickets", "ticket_replies", "ticket_notes"} {
		for _, r := range s[entity] {
			ticket := r["id"]
			if entity != "tickets" {
				ticket = r["tid"]
			}
			for slot, stored := range strings.Split(r["attachment"], ",") {
				if stored == "" {
					continue
				}
				if !DownloadFilenameValid(stored) {
					return fmt.Errorf("invalid ticket attachment filename: %s/%s/%d", entity, r["id"], slot)
				}
				display := stored
				if _, name, ok := strings.Cut(stored, "^"); ok {
					display = name
				}
				if !DownloadFilenameValid(display) {
					return fmt.Errorf("invalid ticket attachment display name: %s/%s/%d", entity, r["id"], slot)
				}
				id++
				refs = append(refs, Record{"id": strconv.FormatInt(id, 10), "ticket_id": ticket, "parent_entity": entity, "parent_id": r["id"], "slot": strconv.Itoa(slot), "stored_name": stored, "filename": display, "create_time": r["create_time"]})
			}
		}
	}
	s["ticket_attachments"] = refs
	return nil
}
