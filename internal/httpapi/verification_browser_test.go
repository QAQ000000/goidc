package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestVerificationBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	capture := os.Getenv("ZJMFGO_BROWSER_CAPTURE_FILE")
	if capture == "" {
		t.Fatal("capture file required")
	}
	defer os.Remove(capture)
	stop := make(chan struct{})
	var worker sync.WaitGroup
	defer func() { close(stop); worker.Wait() }()
	adminBrowserFixture(t, "otp-super", func(db *store.Store, h http.Handler) {
		user(t, db, h, "otp-browser", "customer")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE site_settings SET email_verification_required=true; UPDATE smtp_settings SET configured=true,enabled=true,host='127.0.0.1',port=2525,from_address='fixture@example.test',tls_mode='none'`); err != nil {
			t.Fatal(err)
		}
		worker.Add(1)
		go func() {
			defer worker.Done()
			timer := time.NewTicker(100 * time.Millisecond)
			defer timer.Stop()
			for {
				select {
				case <-stop:
					return
				case <-timer.C:
					job, err := db.ClaimEmailJob(t.Context())
					if err != nil {
						continue
					}
					// Isolated delivery sink: no external SMTP or credentials are used.
					payload, _ := json.Marshal(map[string]any{"recipient": job.Recipient, "body": job.Body, "kind": job.Kind})
					if err = os.WriteFile(capture, payload, 0600); err != nil {
						t.Error(err)
						return
					}
					if err = db.CompleteEmailJob(t.Context(), *job, nil); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}()
	})
}
