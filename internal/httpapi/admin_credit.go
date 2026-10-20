package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) adminCreditPage(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	page, err := s.db.AdminCreditAccountPage(r.Context(), identityFrom(r).ID, r.URL.Query(), limit, offset)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	write(w, http.StatusOK, page)
}

func (s *Server) adminCreditLimit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		LimitCents      int64  `json:"limit_cents"`
		ExpectedVersion int64  `json:"expected_version"`
		Note            string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if in.LimitCents < 0 || in.LimitCents > 100000000000 || in.ExpectedVersion < 1 || len(key) < 8 || len(key) > 128 || len(in.Note) > 240 {
		writeError(w, 400, "invalid_credit_limit", "授权额度、版本、备注或幂等键无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	actor := identityFrom(r).ID
	if !s.lockCreditManager(w, r, tx, id) {
		return
	}
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("admin-credit:%d:%s", actor, key)); err != nil {
		s.fail(w, r, err)
		return
	}
	var version, currentLimit, used int64
	err = tx.QueryRow(r.Context(), `SELECT a.version,a.limit_cents,a.used_cents FROM customer_credit_accounts a JOIN users u ON u.id=a.user_id WHERE a.user_id=$1 AND u.role='customer' AND admin_customer_allowed($2,a.user_id) FOR UPDATE`, id, actor).Scan(&version, &currentLimit, &used)
	if err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, 404, "customer_credit_not_found", "客户信用额账户不存在")
		} else {
			s.fail(w, r, err)
		}
		return
	}
	var oldUser, oldLimit, oldUsed, oldExpected, oldVersion int64
	var oldNote string
	err = tx.QueryRow(r.Context(), `SELECT user_id,limit_after_cents,used_after_cents,expected_version,version_after,note FROM customer_credit_events WHERE actor_id=$1 AND idempotency_key=$2`, actor, key).Scan(&oldUser, &oldLimit, &oldUsed, &oldExpected, &oldVersion, &oldNote)
	if err == nil {
		if oldUser != id || oldLimit != in.LimitCents || oldExpected != in.ExpectedVersion || oldNote != in.Note {
			writeError(w, 409, "idempotency_conflict", "幂等键已用于其他额度请求")
			return
		}
		write(w, 200, map[string]any{"user_id": id, "limit_cents": oldLimit, "used_cents": oldUsed, "available_cents": oldLimit - oldUsed, "version": oldVersion, "replayed": true})
		return
	}
	if err != pgx.ErrNoRows {
		s.fail(w, r, err)
		return
	}
	if version != in.ExpectedVersion {
		writeError(w, 409, "stale_credit", "信用额账户已更新，请刷新后重试")
		return
	}
	if in.LimitCents < used {
		writeError(w, 409, "credit_limit_below_used", "授权额度不能低于已用额度")
		return
	}
	delta := in.LimitCents - currentLimit
	nextVersion := version + 1
	if _, err = tx.Exec(r.Context(), `UPDATE customer_credit_accounts SET limit_cents=$1,version=$2,updated_at=now() WHERE user_id=$3`, in.LimitCents, nextVersion, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO customer_credit_events(user_id,actor_id,event_type,delta_limit_cents,limit_after_cents,used_after_cents,expected_version,version_after,idempotency_key,note) VALUES($1,$2,'limit_adjustment',$3,$4,$5,$6,$7,$8,$9)`, id, actor, delta, in.LimitCents, used, in.ExpectedVersion, nextVersion, key, in.Note); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, actor, "credit.limit_adjusted", "customer_credit_account", id, map[string]any{"limit_cents": in.LimitCents, "used_cents": used, "expected_version": in.ExpectedVersion, "idempotency_key": key, "note": in.Note}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]any{"user_id": id, "limit_cents": in.LimitCents, "used_cents": used, "available_cents": in.LimitCents - used, "version": nextVersion, "replayed": false})
}

func (s *Server) lockCreditManager(w http.ResponseWriter, r *http.Request, tx pgx.Tx, id int64) bool {
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "会话失效")
		return false
	}
	if err = store.LockCreditAdministrator(r.Context(), tx, identityFrom(r).ID, id, tokenHash(cookie.Value)); err != nil {
		s.fail(w, r, err)
		return false
	}
	return true
}
