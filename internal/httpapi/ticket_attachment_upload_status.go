package httpapi

import "net/http"

func (s *Server) ticketAttachmentUploadStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) > 1 {
		writeError(w, 400, "invalid_idempotency_key", "请求键不能重复")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.TicketAttachmentUploadStatus(requestFileContext(r), identityFrom(r).ID, id, key)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}
