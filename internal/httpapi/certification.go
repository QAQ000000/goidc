package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) certification(w http.ResponseWriter, r *http.Request) {
	out, err := s.db.Certification(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) submitCertification(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Type = strings.TrimSpace(strings.ToLower(in.Type))
	out, err := s.db.SubmitCertification(r.Context(), identityFrom(r).ID, in.Type, in.Payload)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusAccepted, out)
}

func (s *Server) adminCertifications(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != "unverified" && status != "pending" && status != "approved" && status != "rejected" {
		writeError(w, 400, "invalid_certification_status", "认证状态无效")
		return
	}
	limit, offset := pagination(r)
	out, err := s.db.AdminCertifications(r.Context(), status, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if out == nil {
		out = []map[string]any{}
	}
	write(w, 200, out)
}

func (s *Server) adminCertificationDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := s.db.AdminCertificationDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}

func (s *Server) reviewCertification(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Decision        string `json:"decision"`
		Note            string `json:"note"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Decision = strings.TrimSpace(strings.ToLower(in.Decision))
	if len(in.Note) > 2000 {
		writeError(w, 400, "invalid_certification_review", "审核备注过长")
		return
	}
	if err := s.db.ReviewCertification(r.Context(), identityFrom(r).ID, id, in.Decision, in.Note, in.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"id": id, "status": in.Decision})
}

func certificationID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}
