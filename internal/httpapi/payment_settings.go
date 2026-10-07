package httpapi

import (
	"context"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) paymentFallback() store.PaymentConfiguration {
	return store.PaymentConfiguration{PaymentSettings: store.PaymentSettings{Enabled: true, MockEnabled: s.cfg.PaymentMockEnabled, APIURL: s.cfg.EpayAPIURL, PID: s.cfg.EpayPID, Types: s.cfg.EpayTypes}, Key: s.cfg.EpayKey, MockSecret: s.cfg.PaymentGatewaySecret, PublicOrigin: s.cfg.PublicOrigin}
}
func (s *Server) paymentConfiguration(ctx context.Context) (store.PaymentConfiguration, error) {
	return s.db.PaymentConfiguration(ctx, s.paymentFallback())
}
func (s *Server) paymentSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.paymentConfiguration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, cfg.PaymentSettings)
}
func (s *Server) updatePaymentSettings(w http.ResponseWriter, r *http.Request) {
	var in store.PaymentSettingsInput
	if !decode(w, r, &in) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.UpdatePaymentSettings(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), in, s.paymentFallback())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
