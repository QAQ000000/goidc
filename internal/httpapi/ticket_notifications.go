package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
)

// true means the request has been answered, including admission and database errors.
func (s *Server) ticketReplay(w http.ResponseWriter, r *http.Request, tx pgx.Tx, key, operation, hash string, ticketID int64) bool {
	raw, replayed, err := store.ReplayTicketAccountMutation(r.Context(), tx, identityFrom(r).ID, key, operation, hash, ticketID)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return true
	}
	if !replayed {
		return false
	}
	status := 200
	if operation == "create" {
		status = 201
	}
	write(w, status, raw)
	return true
}
func saveTicketReceipt(r *http.Request, tx pgx.Tx, key, operation, hash string, id int64, result any) error {
	return store.SaveTicketAccountReceipt(r.Context(), tx, identityFrom(r).ID, key, operation, hash, id, result)
}
func (s *Server) ticketNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	var input *store.TicketNotificationPreferencesInput
	if r.Method == http.MethodPatch {
		input = &store.TicketNotificationPreferencesInput{}
		if !decode(w, r, input) {
			return
		}
		if input.Email == nil || input.SMS == nil || input.Version < 1 {
			writeError(w, 400, "invalid_preferences", "请提供通知偏好和版本")
			return
		}
	}
	result, err := s.db.TicketAccountNotificationPreferences(r.Context(), identityFrom(r).ID, input)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, 200, result)
}
func (s *Server) ticketNotifications(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, offset := pagination(r)
	result, err := s.db.TicketAccountNotifications(r.Context(), id, identityFrom(r).ID, isAdmin(r), limit, offset)
	if err != nil {
		s.failTicketAccount(w, r, err)
		return
	}
	write(w, 200, result)
}
func ticketNotificationOutboxSQL(channel, id, actor, permission string) string {
	return store.TicketNotificationOutboxPredicate(channel, id, actor, permission)
}
