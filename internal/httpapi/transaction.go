package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type txResponse struct {
	status int
	data   any
}

type txHTTPError struct {
	status        int
	code, message string
}

func (e *txHTTPError) Error() string { return fmt.Sprintf("%s: %s", e.code, e.message) }

// Successful responses are sent only after commit. Validation errors preserve
// their endpoint's status/code while causing the callback transaction to roll back.
func (s *Server) withTx(w http.ResponseWriter, r *http.Request, fn func(pgx.Tx) (txResponse, error)) {
	var response txResponse
	err := s.db.WithTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		response, err = fn(tx)
		return err
	})
	if err != nil {
		var failure *txHTTPError
		if errors.As(err, &failure) {
			writeError(w, failure.status, failure.code, failure.message)
		} else {
			s.fail(w, r, err)
		}
		return
	}
	write(w, response.status, response.data)
}
