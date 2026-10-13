package httpapi

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSMSGatewayEvidenceEncryptionLeaseAndRetention(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "sms-evidence-admin", "super_admin")
	var job store.SMSJob
	err := db.Pool.QueryRow(t.Context(), `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at,status,attempts,locked_at) VALUES('+15550000001','payload','test',now()+interval '1 day','sending',1,now()) RETURNING id,attempts,locked_at`).Scan(&job.ID, &job.Attempts, &job.LockedAt)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("\xef\xbb\xbf" + `{"status":"405","msg":"secret code 123456"}`)
	evidence := store.SMSDeliveryEvidence{Provider: "idcsmart", HTTPStatus: 200, Outcome: "business_rejected", Response: body}
	if err = db.SaveSMSDeliveryEvidence(t.Context(), job, evidence); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveSMSDeliveryEvidence(t.Context(), job, evidence); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err = db.Pool.QueryRow(t.Context(), `SELECT response_encrypted FROM sms_delivery_evidence WHERE outbox_id=$1`, job.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("123456")) || bytes.Equal(sealed, body) {
		t.Fatal("raw response stored as plaintext")
	}
	// Authenticate the exact encrypted bytes without exporting a raw-response API.
	domain := "sms:response:" + strconv.FormatInt(job.ID, 10) + ":1:" + job.LockedAt.UTC().Format("2006-01-02T15:04:05.999999999Z")
	plain, err := openSMSEvidence(db.ProvisionEncryptionKey, sealed, domain)
	if err != nil || !bytes.Equal(plain, body) {
		t.Fatal("original bytes not retained", err)
	}
	if _, err = openSMSEvidence(db.ProvisionEncryptionKey, sealed, domain+"other"); err == nil {
		t.Fatal("wrong lease domain accepted")
	}
	job.LockedAt = job.LockedAt.Add(time.Second)
	if err = db.SaveSMSDeliveryEvidence(t.Context(), job, evidence); err != nil {
		t.Fatal(err)
	}
	var count int
	db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM sms_delivery_evidence WHERE outbox_id=$1`, job.ID).Scan(&count)
	if count != 2 {
		t.Fatal("attempt lease collision", count)
	}
	res := request(h, "GET", "/api/v1/admin/sms-outbox", nil, admin, "")
	expect(t, res, 200)
	if strings.Contains(res.Body.String(), "123456") || strings.Contains(res.Body.String(), "response_encrypted") {
		t.Fatal("evidence leaked through admin DTO")
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE sms_delivery_evidence SET expires_at=now()-interval '1 second' WHERE outbox_id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.RecoverStaleSMSJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM sms_delivery_evidence WHERE outbox_id=$1`, job.ID).Scan(&count)
	if count != 0 {
		t.Fatal("expired evidence retained")
	}
}

func openSMSEvidence(key, sealed []byte, domain string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte(domain))
}

func TestSMSChallengeMatchesFiveMinuteTemplate(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "sms-five-minute", "customer")
	actor, _ := user(t, db, h, "sms-five-minute-admin", "super_admin")
	var sessionHash string
	if err := db.Pool.QueryRow(t.Context(), "SELECT token_hash FROM sessions WHERE user_id=$1", actor).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	token := "fixture-token"
	if _, err := db.UpdateSMSSettings(t.Context(), actor, sessionHash, store.SMSSettings{Enabled: true, Provider: "generic_http_json", Endpoint: "http://127.0.0.1:19488/sms"}, &token, false, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE notification_templates SET provider_template_id='approved',body='验证码{{code}},{{expires_minutes}}分钟内有效' WHERE channel='sms'`); err != nil {
		t.Fatal(err)
	}
	response := request(h, "POST", "/api/v1/account/verification/request", map[string]string{"purpose": "bind_phone", "channel": "sms", "recipient": "+15550000002"}, cookie, "")
	expect(t, response, 202)
	result := data(t, response)
	if result["expires_in"].(float64) != 300 {
		t.Fatal("API reports incorrect expiry", result["expires_in"])
	}
	raw := result["challenge_id"].(string)
	var duration float64
	if err := db.Pool.QueryRow(t.Context(), `SELECT extract(epoch FROM expires_at-created_at) FROM verification_challenges WHERE id=$1`, store.VerificationID(raw)).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	if duration < 299 || duration > 301 {
		t.Fatal("challenge expiry differs from template", duration)
	}
	job, err := db.ClaimSMSJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if job.TemplateID != "approved" || !strings.Contains(job.Body, "5分钟内有效") {
		t.Fatal("template binding or expiry not rendered")
	}
}
