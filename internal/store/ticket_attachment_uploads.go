package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const ticketUploadIntentScope = "ticket:attachment-upload-intent"
const ticketUploadResultScope = "ticket:attachment-upload"

type ticketUploadIntent struct {
	TicketID int64  `json:"ticket_id"`
	ObjectID string `json:"object_id"`
}

// An upload's request key is an opaque header, never a URL or object-storage
// key. Immutable intent and result facts share the central receipt mechanism.
func validTicketUploadKey(key string) bool {
	return key == strings.TrimSpace(key) && utf8.ValidString(key) && utf8.RuneCountInString(key) >= 8 && len(key) <= 128
}

func ticketUploadHash(ticket int64, name, mimeType, hash string, size int) string {
	raw, _ := json.Marshal(struct {
		Ticket             int64
		Name, MIME, SHA256 string
		Size               int
	}{ticket, name, mimeType, hash, size})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// The parent lock precedes credential locks, as in existing private-file paths.
// Repeat the scope query after all waits; result reads also work after closing.
func lockTicketUploadAccess(ctx context.Context, tx pgx.Tx, actor, ticket int64, manage, open, write bool) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err != nil {
		return err
	}
	lock := " FOR SHARE OF t"
	if write {
		lock = " FOR UPDATE OF t"
	}
	query := `SELECT t.status,t.merged_ticket_id FROM tickets t
 LEFT JOIN users c ON c.id=$2 LEFT JOIN administrators u ON u.id=$2 LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id
 WHERE t.id=$1 AND ((u.status='active' AND
 (u.role='super_admin' OR (u.role='admin' AND ar.active AND 'tickets.view'=ANY(ar.permissions) AND (NOT $3::boolean OR 'tickets.manage'=ANY(ar.permissions))))
 AND admin_ticket_department_allowed(u.id,t.department_id)
 AND (NOT EXISTS(SELECT 1 FROM users owner WHERE owner.id=t.user_id) OR admin_customer_allowed(u.id,t.user_id)))
 OR (c.status='active' AND t.user_id=c.id AND coalesce((SELECT customer_visible FROM ticket_departments WHERE id=t.department_id),true)))` + lock
	check := func() error {
		var status string
		var merged *int64
		if err := tx.QueryRow(ctx, query, ticket, actor, manage).Scan(&status, &merged); err != nil {
			return err
		}
		if merged != nil {
			return ticketAccountError(409, "ticket_merged", "该工单已合并到其他工单")
		}
		if open && status == "closed" {
			return ticketAccountError(409, "ticket_closed", "工单已关闭")
		}
		return nil
	}
	if err := ticketFileCredentialCurrent(ctx, tx, actor); err != nil {
		return err
	}
	if err := check(); err != nil {
		return err
	}
	if err := LockFileCredential(ctx, tx, actor); err != nil {
		return err
	}
	if err := ticketFileCredentialCurrent(ctx, tx, actor); err != nil {
		return err
	}
	return check()
}

func (s *Store) reserveTicketUpload(ctx context.Context, actor, ticket int64, key, hash string) (ticketUploadIntent, map[string]any, bool, error) {
	var intent ticketUploadIntent
	var confirmed map[string]any
	first := false
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if err := lockTicketUploadAccess(ctx, tx, actor, ticket, true, false, false); err != nil {
			return err
		}
		found, err := ReplayOperation(ctx, tx, actor, ticketUploadResultScope, key, hash, &confirmed)
		if err != nil {
			return err
		}
		if found {
			return lockTicketUploadAccess(ctx, tx, actor, ticket, true, false, false)
		}
		found, err = ReplayOperation(ctx, tx, actor, ticketUploadIntentScope, key, hash, &intent)
		if err != nil {
			return err
		}
		if found {
			if intent.TicketID != ticket {
				return conflict("idempotency_conflict", "相同幂等键对应不同请求")
			}
			return lockTicketUploadAccess(ctx, tx, actor, ticket, true, false, false)
		}
		if err := lockTicketUploadAccess(ctx, tx, actor, ticket, true, true, false); err != nil {
			return err
		}
		intent.TicketID = ticket
		intent.ObjectID, err = fileRandomKey()
		if err != nil {
			return err
		}
		if err = SaveOperation(ctx, tx, actor, ticketUploadIntentScope, key, hash, intent); err != nil {
			return err
		}
		first = true
		return nil
	})
	// A lost reservation Commit acknowledgement never authorizes Put. A later
	// query may see the intent, but no caller may execute that intent again.
	if err != nil {
		if first {
			return ticketUploadIntent{}, nil, false, ticketUploadUnconfirmed()
		}
		return ticketUploadIntent{}, nil, false, err
	}
	return intent, confirmed, first, nil
}

func (s *Store) UploadTicketAccountAttachmentWithKey(ctx context.Context, actor, ticket int64, name, mimeType, hash string, content []byte, key string) (map[string]any, error) {
	if !validTicketUploadKey(key) {
		return nil, ticketAccountError(400, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
	}
	digest := sha256.Sum256(content)
	if len(content) == 0 || len(content) > 10<<20 || hex.EncodeToString(digest[:]) != hash {
		return nil, ticketAccountError(400, "invalid_file", "文件类型或大小无效")
	}
	requestHash := ticketUploadHash(ticket, name, mimeType, hash, len(content))
	intent, confirmed, first, err := s.reserveTicketUpload(ctx, actor, ticket, key, requestHash)
	if err != nil {
		return nil, err
	}
	if confirmed != nil {
		confirmed["state"], confirmed["replayed"] = "confirmed", true
		return confirmed, nil
	}
	if !first {
		return nil, ticketUploadUnconfirmed()
	}
	result, err := s.uploadTicketAccountAttachment(ctx, actor, ticket, name, mimeType, hash, content, key, requestHash, intent.ObjectID)
	if err != nil {
		var denied *TicketAccountError
		if errors.As(err, &denied) || errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		var collision *Conflict
		if errors.As(err, &collision) && (collision.Code == "idempotency_conflict" || collision.Code == "unauthorized") {
			return nil, err
		}
		// The committed intent cannot be executed again. Report the same state
		// for storage uncertainty, transaction rollback and Commit-ack loss;
		// only the result-query API can establish an atomic confirmed receipt.
		return nil, ticketUploadUnconfirmed()
	}
	return result, nil
}

func ticketUploadUnconfirmed() error {
	return ticketAccountError(409, "attachment_upload_unconfirmed", "附件上传结果尚未确认，请查询原请求结果或联系管理员核验；不要重新上传")
}

// Only an atomically committed business receipt proves success. Missing final
// receipts, including inline/audit rollback and external uncertainty, stay
// unconfirmed. This read never touches or repairs the external object.
func (s *Store) TicketAttachmentUploadStatus(ctx context.Context, actor, ticket int64, key string) (map[string]any, error) {
	if !validTicketUploadKey(key) {
		return nil, ticketAccountError(400, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
	}
	var result map[string]any
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if err := lockTicketUploadAccess(ctx, tx, actor, ticket, false, false, false); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT result FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3`, actor, ticketUploadIntentScope, key).Scan(&raw); err != nil {
			return err
		}
		var intent ticketUploadIntent
		if err := json.Unmarshal(raw, &intent); err != nil {
			return err
		}
		if intent.TicketID != ticket {
			return pgx.ErrNoRows
		}
		result = map[string]any{"state": "unconfirmed"}
		err := tx.QueryRow(ctx, `SELECT result FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3`, actor, ticketUploadResultScope, key).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var attachment map[string]any
		if err = json.Unmarshal(raw, &attachment); err != nil {
			return err
		}
		result = map[string]any{"state": "confirmed", "attachment": attachment}
		return nil
	})
	return result, err
}
