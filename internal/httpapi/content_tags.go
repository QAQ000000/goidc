package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) adminContentTags(w http.ResponseWriter, r *http.Request) {
	kind, ok := contentQueryType(w, r, false)
	if !ok {
		return
	}
	s.list(w, r, `SELECT t.*,(SELECT count(*) FROM content_article_tags at WHERE at.tag_id=t.id) AS article_count FROM content_tags t WHERE ($1='' OR t.content_type=$1) ORDER BY t.sort_order,t.id`, kind)
}

func (s *Server) adminSaveContentTag(w http.ResponseWriter, r *http.Request) {
	var id int64
	if r.Method == "PUT" {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var input store.ContentTagInput
	if !decode(w, r, &input) {
		return
	}
	saved, err := s.db.SaveContentTag(r.Context(), identityFrom(r).ID, id, input)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := 200
	if id == 0 {
		status = 201
	}
	write(w, status, map[string]any{"id": saved})
}

func (s *Server) adminDeleteContentTag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.db.DeleteContentTag(r.Context(), identityFrom(r).ID, id, input.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"deleted": true})
}
