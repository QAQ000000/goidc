package httpapi

import (
	"errors"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func accountBusinessFailure(s *Server, w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	var failure *store.AccountBusinessError
	if errors.As(err, &failure) {
		writeError(w, failure.Status, failure.Code, failure.Message)
	} else {
		s.fail(w, r, err)
	}
	return true
}
