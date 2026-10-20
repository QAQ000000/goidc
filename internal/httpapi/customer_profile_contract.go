package httpapi

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Definition consumption is read-only, and does not grant access to CRUD.
func (s *Server) customerFieldDefinitions(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	page, err := s.db.CustomerAccountFieldDefinitions(requestFileContext(r), identityFrom(r).ID, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, page)
}

// An advisory lock also serializes the first request, when no receipt exists yet.
func lockCustomerMutation(r *http.Request, tx pgx.Tx, actor int64, operation, key string) error {
	_, err := tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("customer-mutation:%d:%s:%s", actor, operation, key))
	return err
}
