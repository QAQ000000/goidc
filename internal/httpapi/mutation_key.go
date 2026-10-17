package httpapi

import (
	"net/http"
	"strings"
)

// Public ticket mutations accept an optional body key for older clients. All
// callers share trimming, byte-length bounds and header/body consistency checks.
func mutationKey(w http.ResponseWriter, r *http.Request, body string, required bool) (string, bool) {
	header := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	key := strings.TrimSpace(body)
	if header != "" && key != "" && header != key {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请求键不一致")
		return "", false
	}
	if header != "" {
		key = header
	}
	if (required || key != "") && (len(key) < 8 || len(key) > 128) {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供长度为 8 到 128 的幂等键")
		return "", false
	}
	return key, true
}
