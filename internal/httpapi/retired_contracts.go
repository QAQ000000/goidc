package httpapi

import "net/http"

// Contract routes retain their session and permission checks for old clients,
// but cannot expose documents, replay old receipts, or write historical data.
func (s *Server) retiredContracts(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusGone, "contracts_retired", "合同功能已下线")
}
