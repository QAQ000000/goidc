package httpapi

import (
	_ "github.com/codex/zjmfgo/internal/certification"
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) channelCatalogue(w http.ResponseWriter, r *http.Request) {
	category := channel.Category(strings.TrimSpace(r.URL.Query().Get("category")))
	write(w, http.StatusOK, channel.List(category))
}

func (s *Server) channelInstances(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.ListChannelInstances(r.Context(), r.URL.Query().Get("category"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, items)
}

func (s *Server) createChannelInstance(w http.ResponseWriter, r *http.Request) {
	var in store.ChannelInstanceInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.CreateChannelInstance(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, out)
}

func (s *Server) updateChannelInstance(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var in store.ChannelInstanceInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.UpdateChannelInstance(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), id, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) deleteChannelInstance(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	if err = s.db.DeleteChannelInstance(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), id, in.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"deleted": true})
}
