package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/notify"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// Subscription endpoints are browser capabilities. Only public instance keys
// and opaque subscription metadata leave these authenticated handlers.
func (s *Server) webPushState(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	out, err := s.db.WebPushState(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func (s *Server) webPushHistory(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.WebPushNotificationHistory(r.Context(), identityFrom(r).ID, limit+1, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	write(w, http.StatusOK, map[string]any{"items": items, "has_more": hasMore, "limit": limit, "offset": offset})
}

func (s *Server) saveWebPushSubscription(w http.ResponseWriter, r *http.Request) {
	var in struct {
		InstanceID      string `json:"instance_id"`
		ExpectedVersion *int64 `json:"expected_version"`
		Subscription    struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256DH string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
		} `json:"subscription"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.InstanceID == "" || len(in.InstanceID) > 80 || in.ExpectedVersion == nil || *in.ExpectedVersion < 0 || len(in.Subscription.Endpoint) > 4096 || len(in.Subscription.Keys.P256DH) > 128 || len(in.Subscription.Keys.Auth) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_web_push_subscription", "请提供有效的浏览器订阅和版本")
		return
	}
	if !s.authLimit(w, r, "web_push_subscription", 60) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	uid := identityFrom(r).ID
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.LockFileCredential(requestFileContext(r), tx, uid); err != nil {
			return txResponse{}, err
		}
		subscription := notify.PushSubscription{Endpoint: in.Subscription.Endpoint, P256DH: in.Subscription.Keys.P256DH, Auth: in.Subscription.Keys.Auth}
		out, err := s.db.SaveWebPushSubscription(r.Context(), tx, uid, tokenHash(cookie.Value), in.InstanceID, subscription, *in.ExpectedVersion)
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, uid, "notification.web_push.subscribed", "user", uid, map[string]any{"instance_id": in.InstanceID, "subscription_id": out.ID, "version": out.Version}); err != nil {
			return txResponse{}, err
		}
		return txResponse{http.StatusOK, out}, nil
	})
}

func (s *Server) deleteWebPushSubscription(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "expected_version_required", "请提供订阅版本")
		return
	}
	uid := identityFrom(r).ID
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.LockFileCredential(requestFileContext(r), tx, uid); err != nil {
			return txResponse{}, err
		}
		if err := s.db.DeleteWebPushSubscription(r.Context(), tx, uid, id, in.ExpectedVersion); err != nil {
			return txResponse{}, err
		}
		if err := store.Audit(r.Context(), tx, uid, "notification.web_push.unsubscribed", "user", uid, map[string]any{"subscription_id": id, "version": in.ExpectedVersion + 1}); err != nil {
			return txResponse{}, err
		}
		return txResponse{http.StatusOK, map[string]bool{"unsubscribed": true}}, nil
	})
}
