package httpapi

import (
	"errors"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) registerTicketReportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/reports/tickets/handlers", s.auth(s.ticketHandlerReport, true))
	mux.HandleFunc("POST /api/v1/tickets/{id}/ratings", s.auth(s.rateTicketMessage, false))
}

func (s *Server) rateTicketMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if identityFrom(r).Role != "customer" {
		writeError(w, 403, "customer_rating_only", "只有工单所属客户可以评分")
		return
	}
	var in struct {
		Target  string `json:"target"`
		ReplyID int64  `json:"reply_id"`
		Stars   int    `json:"stars"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Stars < 1 || in.Stars > 5 || (in.Target != "reply" && in.Target != "initial") || (in.Target == "reply" && in.ReplyID < 1) || (in.Target == "initial" && in.ReplyID != 0) {
		writeError(w, 400, "invalid_ticket_rating", "请选择公开管理员回复并评分 1–5 星")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		TicketID int64
		Target   string
		ReplyID  int64
		Stars    int
	}{id, in.Target, in.ReplyID, in.Stars})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var owner int64
		var initialAuthor *int64
		var initialKind string
		err := tx.QueryRow(r.Context(), `SELECT t.user_id,t.created_by,t.initial_author_kind FROM tickets t LEFT JOIN ticket_departments d ON d.id=t.department_id WHERE t.id=$1 AND t.user_id=$2 AND t.merged_ticket_id IS NULL AND coalesce(d.customer_visible,true) FOR SHARE OF t`, id, actor).Scan(&owner, &initialAuthor, &initialKind)
		if errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, &txHTTPError{404, "not_found", "工单不存在或无权评分"}
		}
		if err != nil {
			return txResponse{}, err
		}
		var author int64
		if in.Target == "reply" {
			var kind string
			err = tx.QueryRow(r.Context(), `SELECT author_id,author_kind FROM ticket_replies WHERE id=$1 AND ticket_id=$2 FOR SHARE`, in.ReplyID, id).Scan(&author, &kind)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && kind != "staff" {
				return txResponse{}, &txHTTPError{404, "rating_target_not_found", "没有可评分的公开管理员回复"}
			}
			if err != nil {
				return txResponse{}, err
			}
		} else {
			if initialKind != "staff" || initialAuthor == nil {
				return txResponse{}, &txHTTPError{404, "rating_target_not_found", "初始内容不是可评分的管理员代建内容"}
			}
			author = *initialAuthor
		}
		result := map[string]any{}
		replay, err := store.ReplayOperation(r.Context(), tx, actor, "ticket-rating", key, hash, &result)
		if err != nil {
			return txResponse{}, err
		}
		if replay {
			return txResponse{200, result}, nil
		}
		var reply, initial any
		if in.Target == "reply" {
			reply = in.ReplyID
		} else {
			initial = id
		}
		var ratingID int64
		err = tx.QueryRow(r.Context(), `INSERT INTO ticket_ratings(reply_id,initial_ticket_id,customer_id,staff_id,stars,rated_at) VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT DO NOTHING RETURNING id`, reply, initial, actor, author, in.Stars).Scan(&ratingID)
		if errors.Is(err, pgx.ErrNoRows) {
			return txResponse{}, &txHTTPError{409, "ticket_already_rated", "此回复已经评分，不能重复或修改"}
		}
		if err != nil {
			return txResponse{}, err
		}
		result = map[string]any{"id": ratingID, "target": in.Target, "reply_id": in.ReplyID, "stars": in.Stars}
		if err = store.Audit(r.Context(), tx, actor, "ticket.rated", "ticket", id, result); err != nil {
			return txResponse{}, err
		}
		if err = store.SaveOperation(r.Context(), tx, actor, "ticket-rating", key, hash, result); err != nil {
			return txResponse{}, err
		}
		return txResponse{201, result}, nil
	})
}
