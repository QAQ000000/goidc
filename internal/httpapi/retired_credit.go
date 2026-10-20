package httpapi

import "net/http"

func (s *Server) creditRetired(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusGone, "credit_retired", "信用额度功能已下线")
}
