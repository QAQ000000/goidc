package httpapi

import "net/http"

func (s *Server) adminOrderDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.ReadAdminOrder(r.Context(), identityFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) reviewOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64  `json:"expected_version"`
		Decision        string `json:"decision"`
		Note            string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.ReviewOrder(r.Context(), identityFrom(r).ID, id, input.Decision, input.Note, r.Header.Get("Idempotency-Key"), input.ExpectedVersion)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed {
		status = http.StatusCreated
	}
	write(w, status, result)
}

func (s *Server) updateOrderNotes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Notes           string `json:"notes"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.UpdateOrderNotes(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion, input.Notes, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
func (s *Server) deleteOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, err := s.db.DeleteOrder(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion, input.Reason, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
