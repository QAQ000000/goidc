package httpapi

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Server) requestCancellation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Type   string `json:"type"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.RequestServiceCancellation(r.Context(), identityFrom(r).ID, id, input.Type, input.Reason, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) withdrawCancellation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.db.WithdrawServiceCancellation(r.Context(), identityFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) adminCancellations(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	s.list(w, r, `SELECT cr.id,cr.service_id,cr.user_id,u.display_name AS customer_name,s.name AS service_name,
cr.type,cr.reason,cr.status,cr.resolution_note,cr.created_at,cr.resolved_at
FROM service_cancellation_requests cr JOIN users u ON u.id=cr.user_id JOIN services s ON s.id=cr.service_id
WHERE admin_customer_allowed($3,cr.user_id) ORDER BY cr.id DESC LIMIT $1 OFFSET $2`, limit, offset, identityFrom(r).ID)
}

func (s *Server) adminResolveCancellation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.ResolveServiceCancellation(r.Context(), identityFrom(r).ID, id, strings.TrimSpace(input.Decision), input.Note)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func cancellationForService(r *http.Request, s *Server, serviceID int64) (map[string]any, error) {
	rows, err := s.db.Pool.Query(r.Context(), `SELECT id,service_id,user_id,type,reason,status,resolution_note,created_at,resolved_at,resolved_by
FROM service_cancellation_requests WHERE service_id=$1 ORDER BY id DESC LIMIT 1`, serviceID)
	if err != nil {
		return nil, err
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return item, err
}
