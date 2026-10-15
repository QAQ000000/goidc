package httpapi

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// requestHost resolves the client address used by rate limits, blacklists and
// audit records. X-Forwarded-For/X-Real-IP are honoured only when the socket
// peer belongs to a configured TRUSTED_PROXY_CIDRS network; otherwise the
// request keeps the historical RemoteAddr-derived behaviour.
func (s *Server) requestHost(r *http.Request) string {
	return s.clientIP(r)
}

type blacklistInput struct {
	SubjectType     string     `json:"subject_type"`
	Subject         string     `json:"subject"`
	OperationType   string     `json:"operation_type"`
	RemoteAddr      string     `json:"remote_addr"`
	Reason          string     `json:"reason"`
	BlockedUntil    *time.Time `json:"blocked_until"`
	ExpectedVersion int64      `json:"expected_version"`
}

func validateBlacklist(in *blacklistInput, create bool) error {
	in.SubjectType = strings.TrimSpace(strings.ToLower(in.SubjectType))
	in.Subject = strings.TrimSpace(in.Subject)
	in.OperationType = strings.TrimSpace(strings.ToLower(in.OperationType))
	in.RemoteAddr = strings.TrimSpace(in.RemoteAddr)
	in.Reason = strings.TrimSpace(in.Reason)
	if in.OperationType == "" {
		in.OperationType = "login"
	}
	if (in.SubjectType != "username" && in.SubjectType != "ip") || in.Subject == "" || len(in.Subject) > 254 || in.OperationType != "login" || len(in.Reason) > 500 {
		return &store.Conflict{Code: "invalid_blacklist", Message: "黑名单字段无效"}
	}
	if in.SubjectType == "ip" && net.ParseIP(in.Subject) == nil {
		return &store.Conflict{Code: "invalid_blacklist", Message: "IP 地址无效"}
	}
	if create && in.ExpectedVersion != 0 || !create && in.ExpectedVersion < 1 {
		return &store.Conflict{Code: "invalid_blacklist", Message: "黑名单版本无效"}
	}
	return nil
}

func (s *Server) adminBlacklist(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := strings.TrimSpace(r.URL.Query().Get("active"))
	where := "($1='' OR subject ILIKE '%'||$1||'%' OR reason ILIKE '%'||$1||'%' OR remote_addr ILIKE '%'||$1||'%')"
	args := []any{q}
	if active != "" {
		if active != "true" && active != "false" {
			writeError(w, http.StatusBadRequest, "invalid_active", "启用状态无效")
			return
		}
		args = append(args, active == "true")
		where += " AND (($2 AND (blocked_until IS NULL OR blocked_until>now())) OR (NOT $2 AND blocked_until IS NOT NULL AND blocked_until<=now()))"
	}
	var total int64
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT count(*) FROM auth_blacklist WHERE "+where, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	query := "SELECT id,subject_type,subject,operation_type,remote_addr,reason,blocked_until,version,created_at,updated_at,(blocked_until IS NULL OR blocked_until>now()) AS active FROM auth_blacklist WHERE " + where + " ORDER BY id DESC LIMIT $" + strconv.Itoa(len(pageArgs)-1) + " OFFSET $" + strconv.Itoa(len(pageArgs))
	rows, err := s.db.Pool.Query(r.Context(), query, pageArgs...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if items == nil {
		items = []map[string]any{}
	}
	write(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *Server) saveBlacklist(w http.ResponseWriter, r *http.Request) {
	var in blacklistInput
	if !decode(w, r, &in) {
		return
	}
	created := r.Method == http.MethodPost
	if err := validateBlacklist(&in, created); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64(0)
	if !created {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "security.blacklist.create"
	if !created {
		operation = "security.blacklist.update:" + strconv.FormatInt(id, 10)
	}
	hash := settingMutationHash(struct {
		ID int64
		In blacklistInput
	}{id, in})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var version int64
	if created {
		err = tx.QueryRow(r.Context(), `INSERT INTO auth_blacklist(subject_type,subject,operation_type,remote_addr,reason,blocked_until) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(subject_type,subject,operation_type) DO UPDATE SET remote_addr=excluded.remote_addr,reason=excluded.reason,blocked_until=excluded.blocked_until,version=auth_blacklist.version+1,updated_at=now() RETURNING id,version`, in.SubjectType, in.Subject, in.OperationType, in.RemoteAddr, in.Reason, in.BlockedUntil).Scan(&id, &version)
	} else {
		var current int64
		err = tx.QueryRow(r.Context(), "SELECT version FROM auth_blacklist WHERE id=$1 FOR UPDATE", id).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "黑名单记录不存在")
			return
		}
		if err == nil && current != in.ExpectedVersion {
			writeError(w, http.StatusConflict, "version_conflict", "黑名单记录已变更，请重新加载")
			return
		}
		if err == nil {
			err = tx.QueryRow(r.Context(), `UPDATE auth_blacklist SET subject_type=$1,subject=$2,operation_type=$3,remote_addr=$4,reason=$5,blocked_until=$6,version=version+1,updated_at=now() WHERE id=$7 RETURNING version`, in.SubjectType, in.Subject, in.OperationType, in.RemoteAddr, in.Reason, in.BlockedUntil, id).Scan(&version)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "version": version}
	action := "security.blacklist.created"
	if !created {
		action = "security.blacklist.updated"
	}
	if err = store.Audit(r.Context(), tx, actor, action, "auth_blacklist", id, map[string]any{"subject_type": in.SubjectType, "subject": in.Subject, "blocked_until": in.BlockedUntil}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], result)
}

func (s *Server) deleteBlacklist(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_blacklist", "黑名单版本无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	operation := "security.blacklist.delete:" + strconv.FormatInt(id, 10)
	hash := settingMutationHash(struct{ ID, Version int64 }{id, in.ExpectedVersion})
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, operation, key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, http.StatusOK, replay)
		return
	}
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM auth_blacklist WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "黑名单记录不存在")
			return
		}
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, http.StatusConflict, "version_conflict", "黑名单记录已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM auth_blacklist WHERE id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = store.Audit(r.Context(), tx, actor, "security.blacklist.deleted", "auth_blacklist", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = saveSettingOperationTx(r, tx, actor, operation, key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
