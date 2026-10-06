package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	s.list(w, r, "SELECT id,name,prefix,last_used_at,expires_at,revoked_at,created_at FROM api_keys WHERE user_id=$1 ORDER BY id DESC", identityFrom(r).ID)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string `json:"name"`
		ExpiresAt string `json:"expires_at"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_api_key", "密钥名称长度无效")
		return
	}
	var expiresAt *time.Time
	if strings.TrimSpace(input.ExpiresAt) != "" {
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(input.ExpiresAt))
		if err != nil || !value.After(time.Now().UTC()) {
			writeError(w, http.StatusBadRequest, "invalid_api_key_expiry", "过期时间必须是未来的 RFC3339 时间")
			return
		}
		expiresAt = &value
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.fail(w, r, err)
		return
	}
	secret := "zjmfgo_" + hex.EncodeToString(raw)
	prefix := "zjmfgo_" + hex.EncodeToString(raw[:8])
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	if err = tx.QueryRow(r.Context(), "INSERT INTO api_keys(user_id,name,prefix,secret_hash,expires_at) VALUES($1,$2,$3,$4,$5) RETURNING id", identityFrom(r).ID, input.Name, prefix, tokenHash(secret), expiresAt).Scan(&id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "api_key.created", "api_key", id, map[string]any{"name": input.Name, "prefix": prefix, "expires_at": expiresAt}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, map[string]any{"id": id, "name": input.Name, "prefix": prefix, "secret": secret, "expires_at": expiresAt})
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var keyID int64
	if err = tx.QueryRow(r.Context(), "UPDATE api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND user_id=$2 RETURNING id", id, identityFrom(r).ID).Scan(&keyID); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, http.StatusNotFound, "not_found", "密钥不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "api_key.revoked", "api_key", keyID, map[string]any{}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"id": keyID, "revoked": true})
}
