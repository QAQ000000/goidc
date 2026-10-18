package httpapi

import "net/http"

func reportJSONFormat(w http.ResponseWriter, r *http.Request) bool {
	if format := r.URL.Query().Get("format"); format != "" && format != "json" {
		writeError(w, http.StatusBadRequest, "invalid_report_format", "报表格式只支持 JSON")
		return false
	}
	return true
}

func writeReport(w http.ResponseWriter, r *http.Request, data any) {
	if !reportJSONFormat(w, r) {
		return
	}
	write(w, http.StatusOK, data)
}
