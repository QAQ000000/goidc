package httpapi

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/store"
)

func manualResourceQuery(w http.ResponseWriter, r *http.Request, list bool) (store.ManualResourceFilter, bool) {
	filter := store.ManualResourceFilter{Limit: 25}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_manual_resource_query", "人工资源查询参数无效")
		return filter, false
	}
	for name, entries := range values {
		if !list || len(entries) != 1 || (name != "q" && name != "status" && name != "binding" && name != "limit" && name != "offset") {
			writeError(w, http.StatusBadRequest, "invalid_manual_resource_query", "人工资源查询参数无效")
			return filter, false
		}
	}
	filter.Q = strings.TrimSpace(values.Get("q"))
	filter.Status, filter.Binding = values.Get("status"), values.Get("binding")
	valid := utf8.ValidString(filter.Q) && utf8.RuneCountInString(filter.Q) <= 120
	if _, present := values["status"]; present {
		valid = valid && (filter.Status == "available" || filter.Status == "disabled" || filter.Status == "retired")
	}
	if _, present := values["binding"]; present {
		valid = valid && (filter.Binding == "bound" || filter.Binding == "unbound")
	}
	for name, target := range map[string]*int{"limit": &filter.Limit, "offset": &filter.Offset} {
		if entries, present := values[name]; present {
			value := entries[0]
			if value == "" || strings.IndexFunc(value, func(c rune) bool { return c < '0' || c > '9' }) >= 0 {
				valid = false
				continue
			}
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 0 || (name == "limit" && (parsed < 1 || parsed > 100)) {
				valid = false
				continue
			}
			*target = parsed
		}
	}
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_manual_resource_filter", "人工资源筛选条件无效")
		return filter, false
	}
	return filter, true
}

func (s *Server) manualResources(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	filter, ok := manualResourceQuery(w, r, r.Method == http.MethodGet)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		page, err := s.db.ListManualResources(requestFileContext(r), identityFrom(r).ID, filter)
		if !accountBusinessFailure(s, w, r, err) {
			write(w, http.StatusOK, page)
		}
		return
	}
	var input store.ManualResourceInput
	if !decode(w, r, &input) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, replayed, err := s.db.CreateManualResource(requestFileContext(r), identityFrom(r).ID, key, input)
	if accountBusinessFailure(s, w, r, err) {
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) manualResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if _, ok := manualResourceQuery(w, r, false); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		result, err := s.db.ManualResource(requestFileContext(r), identityFrom(r).ID, id)
		if !accountBusinessFailure(s, w, r, err) {
			write(w, http.StatusOK, result)
		}
		return
	}
	var input store.ManualResourcePatchInput
	if !decode(w, r, &input) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.UpdateManualResource(requestFileContext(r), identityFrom(r).ID, id, key, input)
	if !accountBusinessFailure(s, w, r, err) {
		write(w, http.StatusOK, result)
	}
}

func (s *Server) releaseManualResource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if _, ok := manualResourceQuery(w, r, false); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input store.ManualResourceReleaseInput
	if !decode(w, r, &input) {
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	result, err := s.db.ReleaseManualResource(requestFileContext(r), identityFrom(r).ID, id, key, input)
	if !accountBusinessFailure(s, w, r, err) {
		write(w, http.StatusOK, result)
	}
}
