package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) topupOptions(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r).Role != "customer" {
		writeError(w, 403, "permission_denied", "仅客户可充值")
		return
	}
	cookie, _ := r.Cookie("zjmfgo_session")
	out, err := s.db.OnlineTopupOptions(store.WithTopupSession(r.Context(), tokenHash(cookie.Value)), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
func (s *Server) createOnlineTopup(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r).Role != "customer" {
		writeError(w, 403, "permission_denied", "仅客户可充值")
		return
	}
	var in store.OnlineTopupInput
	if !decode(w, r, &in) {
		return
	}
	cookie, _ := r.Cookie("zjmfgo_session")
	out, err := s.db.CreateOnlineTopup(store.WithTopupSession(r.Context(), tokenHash(cookie.Value)), identityFrom(r).ID, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 201
	if out.Replayed {
		status = 200
	}
	write(w, status, out)
}

func (s *Server) cancelOnlineTopup(w http.ResponseWriter, r *http.Request) {
	if identityFrom(r).Role != "customer" {
		writeError(w, 403, "permission_denied", "仅客户可充值")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in store.OnlineTopupCancelInput
	if !decode(w, r, &in) {
		return
	}
	cookie, _ := r.Cookie("zjmfgo_session")
	out, err := s.db.CancelOnlineTopup(store.WithTopupSession(r.Context(), tokenHash(cookie.Value)), identityFrom(r).ID, id, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
