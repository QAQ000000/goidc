package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

func affiliateRequestHash(value any) string {
	raw, _ := json.Marshal(value)
	return tokenHash(string(raw))
}

func (s *Server) affiliateClick(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "affiliate-click", 120) {
		return
	}
	code := strings.TrimSpace(r.PathValue("code"))
	token := ""
	if cookie, err := r.Cookie("zjmfgo_affiliate_session"); err == nil {
		if decoded, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value); decodeErr == nil && len(decoded) == 24 {
			token = cookie.Value
		}
	}
	if token == "" {
		tokenBytes := make([]byte, 24)
		if _, err := rand.Read(tokenBytes); err != nil {
			s.fail(w, r, err)
			return
		}
		token = base64.RawURLEncoding.EncodeToString(tokenBytes)
	}
	result, err := s.db.RecordAffiliateClick(r.Context(), code, tokenHash(token), r.Referer(), r.URL.Path, r.UserAgent())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !result.Valid {
		write(w, http.StatusOK, result)
		return
	}
	maxAge := result.AttributionDays * 24 * 60 * 60
	if maxAge < 24*60*60 {
		maxAge = 30 * 24 * 60 * 60
	}
	firstCode := ""
	if cookie, cookieErr := r.Cookie("zjmfgo_affiliate_code"); cookieErr == nil && validAffiliateCookieCode(cookie.Value) {
		firstCode = cookie.Value
	}
	if firstCode == "" {
		http.SetCookie(w, &http.Cookie{Name: "zjmfgo_affiliate_code", Value: code, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Secure: s.cfg.SecureCookie})
	}
	http.SetCookie(w, &http.Cookie{Name: "zjmfgo_affiliate_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Secure: s.cfg.SecureCookie})
	write(w, http.StatusOK, result)
}

func validAffiliateCookieCode(value string) bool {
	if len(value) < 12 || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func (s *Server) affiliateOverview(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.AffiliateOverview(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) affiliateCommissions(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.ListAffiliateCommissions(r.Context(), identityFrom(r).ID, limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, items)
}

func (s *Server) applyAffiliate(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.ApplyAffiliateAccount(r.Context(), identityFrom(r).ID, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusCreated, result)
}

func (s *Server) affiliateLinks(w http.ResponseWriter, r *http.Request) {
	items, err := s.db.ListAffiliateLinks(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.Method == http.MethodGet {
		write(w, http.StatusOK, items)
		return
	}
	var input struct {
		LandingPath string `json:"landing_path"`
		ProductID   *int64 `json:"product_id"`
		Note        string `json:"note"`
	}
	if !decode(w, r, &input) {
		return
	}
	result, replay, err := s.db.CreateAffiliateLink(r.Context(), identityFrom(r).ID, r.Header.Get("Idempotency-Key"), affiliateRequestHash(input), strings.TrimSpace(input.LandingPath), strings.TrimSpace(input.Note), input.ProductID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) updateAffiliateLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		LandingPath string `json:"landing_path"`
		Note        string `json:"note"`
		Status      string `json:"status"`
	}
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "请提供幂等键")
		return
	}
	result, err := s.db.UpdateAffiliateLink(r.Context(), identityFrom(r).ID, id, strings.TrimSpace(r.Header.Get("Idempotency-Key")), affiliateRequestHash(input), strings.TrimSpace(input.LandingPath), strings.TrimSpace(input.Note), strings.TrimSpace(input.Status))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, result)
}

func (s *Server) adminAffiliateAccounts(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.ListAdminAffiliateAccounts(r.Context(), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, items)
}

func (s *Server) adminAffiliateAttributions(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.ListAdminAffiliateAttributions(r.Context(), limit, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, items)
}

func (s *Server) adminAffiliateAccountStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Status          string `json:"status"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "请提供幂等键")
		return
	}
	if err := s.db.ApproveAffiliateAccount(r.Context(), identityFrom(r).ID, id, strings.TrimSpace(r.Header.Get("Idempotency-Key")), strings.TrimSpace(input.Status), input.ExpectedVersion); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"id": id, "status": input.Status, "version": input.ExpectedVersion + 1})
}
