package httpapi

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"testing"
)

func TestAdminReviewFixesBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "review-fixes-super", func(db *store.Store, h http.Handler) {
		var owner int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,status) VALUES('browser-referrer','browser-referrer@example.test','unused','Browser Referrer','customer','active') RETURNING id`).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		var account int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO affiliate_accounts(user_id,program_id,account_type,status) SELECT $1,id,'referrer','approved' FROM affiliate_programs LIMIT 1 RETURNING id`, owner).Scan(&account); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 125; i++ {
			var id int64
			if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,status) VALUES($1,$1||'@example.test','unused',$2,'customer','active') RETURNING id`, fmt.Sprintf("browser-client-%03d", i), fmt.Sprintf("验收客户%03d", i)).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO certification_profiles(user_id,status) VALUES($1,'pending');`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_accounts(user_id,program_id,account_type,status) SELECT $1,id,'referrer','pending' FROM affiliate_programs LIMIT 1`, id); err != nil {
				t.Fatal(err)
			}
		}
		block, err := aes.NewCipher(db.ProvisionEncryptionKey)
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		nonce := make([]byte, gcm.NonceSize())
		if _, err = rand.Read(nonce); err != nil {
			t.Fatal(err)
		}
		aad := "browser-acceptance-target"
		sealed := gcm.Seal(nonce, nonce, []byte(`{"target":"6222021234567890123","holder":"验收收款人"}`), []byte(aad))
		if _, err = db.Pool.Exec(t.Context(), `INSERT INTO affiliate_withdrawals(account_id,amount_cents,fee_cents,payout_cents,payout_method,payout_target_ciphertext,payout_target_aad,payout_target_masked,payout_target_snapshot,idempotency_key,status) VALUES($1,1000,0,1000,'bank',$2,$3,'****0123','{}','browser-approved-withdrawal','approved')`, account, sealed, aad); err != nil {
			t.Fatal(err)
		}
	})
}
