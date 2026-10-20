package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
)

func customerReferrerQuery(w http.ResponseWriter, r *http.Request, kind string) (store.CustomerReferrerFilter, bool) {
	f := store.CustomerReferrerFilter{Limit: 25}
	if !utf8.ValidString(r.URL.RawQuery) || strings.ContainsRune(r.URL.RawQuery, 0) {
		writeError(w, 400, "invalid_referrer_filter", "推荐人查询参数无效")
		return f, false
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, 400, "invalid_referrer_filter", "推荐人查询参数无效")
		return f, false
	}
	valid := true
	for name, entries := range values {
		if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
			valid = false
		}
		for _, entry := range entries {
			if !utf8.ValidString(entry) || strings.ContainsRune(entry, 0) {
				valid = false
			}
		}
		if len(entries) != 1 || kind == "current" || (name != "limit" && name != "offset" && (name != "q" || kind != "options")) {
			valid = false
		}
	}
	f.Q = strings.TrimSpace(values.Get("q"))
	valid = valid && utf8.ValidString(f.Q) && utf8.RuneCountInString(f.Q) <= 120
	for name, target := range map[string]*int{"limit": &f.Limit, "offset": &f.Offset} {
		if entries, ok := values[name]; ok && len(entries) == 1 {
			v := entries[0]
			if v == "" || strings.IndexFunc(v, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
				valid = false
				continue
			}
			n, e := strconv.Atoi(v)
			if e != nil || n < 0 || (name == "limit" && (n < 1 || n > 100)) {
				valid = false
				continue
			}
			*target = n
		}
	}
	if !valid {
		writeError(w, 400, "invalid_referrer_filter", "推荐人查询条件无效")
		return f, false
	}
	return f, true
}
func (s *Server) adminCustomerReferrer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, ok = customerReferrerQuery(w, r, "current"); !ok {
		return
	}
	if r.Method == http.MethodGet {
		out, err := s.db.AdminCustomerReferrer(requestFileContext(r), identityFrom(r).ID, id)
		if !accountBusinessFailure(s, w, r, err) {
			write(w, 200, out)
		}
		return
	}
	var input struct {
		ReferrerCustomerID json.RawMessage `json:"referrer_customer_id"`
		ExpectedVersion    int64           `json:"expected_version"`
		Reason             string          `json:"reason"`
	}
	if !decode(w, r, &input) {
		return
	}
	var referrer *int64
	if len(input.ReferrerCustomerID) == 0 || json.Unmarshal(input.ReferrerCustomerID, &referrer) != nil || (referrer != nil && *referrer < 1) {
		writeError(w, 400, "invalid_referrer", "请明确提交推荐客户编号，清除时填写null")
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		writeError(w, 400, "invalid_idempotency_key", "请提交单个稳定请求键")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.SetAdminCustomerReferrer(requestFileContext(r), identityFrom(r).ID, id, key, store.CustomerReferrerInput{ReferrerCustomerID: referrer, ExpectedVersion: input.ExpectedVersion, Reason: input.Reason})
	if !accountBusinessFailure(s, w, r, err) {
		write(w, 200, out)
	}
}
func (s *Server) adminCustomerReferrerOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, ok := customerReferrerQuery(w, r, "options")
	if !ok {
		return
	}
	out, err := s.db.AdminCustomerReferrerOptions(requestFileContext(r), identityFrom(r).ID, id, f)
	if !accountBusinessFailure(s, w, r, err) {
		write(w, 200, out)
	}
}
func (s *Server) adminCustomerReferrerHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	f, ok := customerReferrerQuery(w, r, "history")
	if !ok {
		return
	}
	out, err := s.db.AdminCustomerReferrerHistory(requestFileContext(r), identityFrom(r).ID, id, f)
	if !accountBusinessFailure(s, w, r, err) {
		write(w, 200, out)
	}
}
