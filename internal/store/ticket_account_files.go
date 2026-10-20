package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

func ticketFileCredentialCurrent(ctx context.Context, tx pgx.Tx, actor int64) error {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.Hash == "" {
		return ticketAccountError(401, "unauthorized", "登录已失效，请重新登录")
	}
	query := `SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp())`
	if credential.APIKey {
		query = `SELECT EXISTS(SELECT 1 FROM api_keys WHERE secret_hash=$1 AND user_id=$2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp()))`
	}
	var valid bool
	if err := tx.QueryRow(ctx, query, credential.Hash, actor).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ticketAccountError(401, "unauthorized", "登录已失效，请重新登录")
	}
	table, err := authenticationTable(ctx, tx, actor)
	if err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1 AND status='active')", actor).Scan(&active); err != nil {
		return err
	}
	if !active {
		return ticketAccountError(401, "unauthorized", "登录已失效，请重新登录")
	}
	return nil
}
func (s *Store) UploadTicketAccountAttachment(ctx context.Context, actor, id int64, name, mimeType, hash string, content []byte) (map[string]any, error) {
	return s.uploadTicketAccountAttachment(ctx, actor, id, name, mimeType, hash, content, "", "", "")
}

func (s *Store) uploadTicketAccountAttachment(ctx context.Context, actor, id int64, name, mimeType, hash string, content []byte, key, requestHash, objectKey string) (map[string]any, error) {
	var result map[string]any
	err := s.WithTx(ctx, func(tx pgx.Tx) error {

		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
			return err
		}
		actorID := actor
		var ownerID int64
		var status string
		const scope = `SELECT t.user_id,t.status FROM tickets t
 LEFT JOIN users c ON c.id=$2 LEFT JOIN administrators u ON u.id=$2 LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id
 WHERE t.id=$1 AND t.merged_ticket_id IS NULL AND (
	 (u.status='active' AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.view'=ANY(ar.permissions) AND 'tickets.manage'=ANY(ar.permissions))) AND admin_ticket_department_allowed(u.id,t.department_id)
 AND (NOT EXISTS(SELECT 1 FROM users owner WHERE owner.id=t.user_id) OR admin_customer_allowed(u.id,t.user_id)))
 OR (c.status='active' AND t.user_id=c.id AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=t.department_id),true))) FOR UPDATE OF t`
		authorize := func() error {
			if err := ticketFileCredentialCurrent(ctx, tx, actorID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, scope, id, actorID).Scan(&ownerID, &status); err != nil {
				return err
			}
			if status == "closed" {
				return ticketAccountError(409, "ticket_closed", "工单已关闭")
			}
			return nil
		}
		if err := authorize(); err != nil {
			return err
		}
		var attachmentID int64
		var createdAt any
		if key != "" {
			var saved map[string]any
			replayed, err := ReplayOperation(ctx, tx, actor, ticketUploadResultScope, key, requestHash, &saved)
			if err != nil {
				return err
			}
			if replayed {
				return conflict("attachment_upload_unconfirmed", "附件上传结果需要核验")
			}
			if err := authorize(); err != nil {
				return err
			}
		}
		stored, err := s.persistFileWithKey(ctx, tx, "ticket_attachments", content, objectKey)
		if err != nil {
			return err
		}
		if err = LockFileCredential(ctx, tx, actor); err != nil {
			return err
		}
		// PersistFile can wait on local or remote I/O. Recheck the current actor
		// and ticket scope instead of reusing the middleware's role snapshot.
		if err = authorize(); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `INSERT INTO ticket_attachments(ticket_id,uploader_id,filename,mime_type,size_bytes,sha256,content,storage_object_id)
VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,created_at`, id, actor, name, mimeType, len(content), hash, stored.Content, stored.ObjectID).Scan(&attachmentID, &createdAt); err != nil {
			return err
		}
		if err = Audit(ctx, tx, actor, "ticket.attachment.uploaded", "ticket_attachment", attachmentID, map[string]any{"ticket_id": id, "filename": name, "mime_type": mimeType, "size_bytes": len(content), "sha256": hash, "customer_id": ownerID}); err != nil {
			return err
		}

		result = map[string]any{"id": attachmentID, "ticket_id": id, "uploader_id": actor, "filename": name, "mime_type": mimeType, "size_bytes": len(content), "sha256": hash, "created_at": createdAt}
		if key != "" {
			if err := SaveOperation(ctx, tx, actor, ticketUploadResultScope, key, requestHash, result); err != nil {
				return err
			}
			result["state"], result["replayed"] = "confirmed", false
		}
		return nil
	})
	return result, err
}

type TicketAttachmentFile struct {
	Name, MIMEType string
	Size           int64
	Content        []byte
}

func (s *Store) DownloadTicketAccountAttachment(ctx context.Context, actor, ticketID, attachment int64, admin bool) (TicketAttachmentFile, error) {
	var name, mimeType string
	var size int64
	var content []byte
	var objectID *string
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return TicketAttachmentFile{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return TicketAttachmentFile{}, err
	}
	const scope = ` FROM ticket_attachments a JOIN tickets t ON t.id=a.ticket_id
WHERE a.id=$1 AND a.ticket_id=$2 AND t.merged_ticket_id IS NULL AND (($3::boolean AND admin_ticket_department_allowed($4,t.department_id)
 AND EXISTS(SELECT 1 FROM administrators u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$4 AND u.status='active'
 AND (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.view'=ANY(ar.permissions)))))
 OR (t.user_id=$4 AND a.note_id IS NULL AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=t.department_id),true)))`
	// Merge and upload lock the parent first. Do not let a joined row-lock
	// plan acquire an attachment before its parent and invert that order.
	var parentID int64
	if err = tx.QueryRow(ctx, `SELECT t.id`+scope+` FOR SHARE OF t`, attachment, ticketID, admin, actor).Scan(&parentID); err != nil {
		return TicketAttachmentFile{}, err
	}
	err = tx.QueryRow(ctx, `SELECT a.filename,a.mime_type,a.size_bytes,a.content,a.storage_object_id`+scope+` FOR SHARE OF a`, attachment, ticketID, admin, actor).Scan(&name, &mimeType, &size, &content, &objectID)
	if err != nil {
		return TicketAttachmentFile{}, err
	}
	content, err = s.ReadFileContent(ctx, tx, "ticket_attachments", content, objectID)
	if err != nil {
		return TicketAttachmentFile{}, err
	}
	if err = LockFileCredential(ctx, tx, actor); err != nil {
		return TicketAttachmentFile{}, err
	}
	if err = ticketFileCredentialCurrent(ctx, tx, actor); err != nil {
		return TicketAttachmentFile{}, err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+scope+`)`, attachment, ticketID, admin, actor).Scan(&allowed); err != nil {
		return TicketAttachmentFile{}, err
	}
	if !allowed {
		return TicketAttachmentFile{}, ticketAccountError(404, "not_found", "附件不存在或当前账户无法访问")
	}
	if err = Audit(ctx, tx, actor, "ticket.attachment.downloaded", "ticket_attachment", attachment, map[string]any{"ticket_id": ticketID, "filename": name, "size_bytes": size}); err != nil {
		return TicketAttachmentFile{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return TicketAttachmentFile{}, err
	}
	return TicketAttachmentFile{Name: name, MIMEType: mimeType, Size: size, Content: content}, nil
}
