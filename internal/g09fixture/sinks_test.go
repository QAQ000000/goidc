package g09fixture

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testJournal(t *testing.T) *Journal {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := NewJournal(filepath.Join(dir, "accepted.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}
func TestSMTPProductionClientAcceptance(t *testing.T) {
	j := testJournal(t)
	sink, err := StartSMTP(j)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	message := []byte("From: fixture@example.test\r\nTo: staff@example.test\r\nSubject: g09\r\n\r\nprivate body\r\n")
	if err = smtp.SendMail(sink.Addr(), nil, "fixture@example.test", []string{"staff@example.test"}, message); err != nil {
		t.Fatal(err)
	}
	if err = smtp.SendMail(sink.Addr(), nil, "fixture@example.test", []string{"staff@example.com"}, message); err == nil {
		t.Fatal("accepted external recipient")
	}
	records := j.Snapshot()
	if len(records) != 1 || records[0].Channel != "email" || records[0].Sequence != 1 {
		t.Fatalf("unexpected records: %+v", records)
	}
	if records[0].RecipientHash != digest("staff@example.test") {
		t.Fatal("SMTP envelope recipient was not normalized for entity correlation")
	}
	b, err := os.ReadFile(j.file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "private body") || strings.Contains(string(b), "staff@example.test") {
		t.Fatal("plaintext leaked")
	}
	var persisted Acceptance
	if err = json.Unmarshal(b, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != records[0] {
		t.Fatal("success returned before matching persisted evidence")
	}
}
func TestSMSAuthValidationAndPersistence(t *testing.T) {
	j := testJournal(t)
	handler, err := SMSHandler(j, "private-token")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	for _, tc := range []struct {
		token, body string
		status      int
	}{
		{"wrong", `{"recipient":"+999000000001","message":"secret","kind":"ticket"}`, 401},
		{"private-token", `{"recipient":"+8613800000000","message":"secret","kind":"ticket"}`, 400},
		{"private-token", `{"recipient":"+999000000001","message":"secret","kind":"ticket","unknown":1}`, 400},
		{"private-token", `{"recipient":"+999000000001","message":"secret","kind":"ticket"} {}`, 400},
		{"private-token", `{"recipient":"+999000000001","message":"secret","kind":"ticket"}`, 200},
	} {
		r, _ := http.NewRequest("POST", server.URL, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("got %d want %d", resp.StatusCode, tc.status)
		}
	}
	if records := j.Snapshot(); len(records) != 1 || records[0].Channel != "sms" {
		t.Fatalf("unexpected accepted evidence: %+v", records)
	}
}
func TestJournalFailureNeverReturnsSuccess(t *testing.T) {
	j := testJournal(t)
	j.Close()
	handler, _ := SMSHandler(j, "token")
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"recipient":"+999000000001","message":"secret","kind":"ticket"}`))
	r.Header.Set("Authorization", "Bearer token")
	handler.ServeHTTP(w, r)
	if w.Code != 503 || len(j.Snapshot()) != 0 {
		t.Fatal("unpersisted SMS accepted")
	}
	sink, err := StartSMTP(j)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if err = smtp.SendMail(sink.Addr(), nil, "fixture@example.test", []string{"staff@example.test"}, []byte("Subject: fixture\r\n\r\nsecret\r\n")); err == nil {
		t.Fatal("unpersisted email accepted")
	}
}
func TestPrivateControlRejectsInjectionAndReplay(t *testing.T) {
	for _, raw := range []string{
		`{"runId":"run","sequence":2,"action":"snapshot"}`,
		`{"runId":"other","sequence":1,"action":"snapshot"}`,
		`{"runId":"run","sequence":1,"action":"snapshot","sql":"delete"}`,
		`{"runId":"run","sequence":1,"action":"shell"}`,
		`{"runId":"run","sequence":1,"action":"start-workers","slot":2}`,
		`{"runId":"run","sequence":1,"action":"snapshot","slot":0}`,
		`{"RunId":"run","sequence":1,"action":"snapshot"}`,
		`{"runId":"other","RunId":"run","sequence":1,"action":"snapshot"}`,
		`{"runId":"run","sequence":1,"Action":"snapshot"}`,
	} {
		if _, err := ValidateRequest([]byte(raw), "run", 0); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	raw := []byte(`{"runId":"run","sequence":1,"action":"snapshot"}`)
	if _, err := ValidateRequest(raw, "run", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateRequest(raw, "run", 1); err == nil {
		t.Fatal("replayed request")
	}
	if _, err := ValidateRequest([]byte(`{"runId":"run","sequence":0,"action":"snapshot"}`), "run", ^uint64(0)); err == nil {
		t.Fatal("sequence wrapped after uint64 exhaustion")
	}
}

func TestJournalBudgetAndPersistenceErrorsRemainLatched(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		j := testJournal(t)
		j.records = make([]Acceptance, maxAcceptances)
		if err := j.accept("sms", "+999000000001", "body"); err == nil || j.Err() == nil {
			t.Fatal("unbounded acceptance journal")
		}
		j.records = nil
		if err := j.accept("sms", "+999000000001", "body"); err == nil {
			t.Fatal("journal resumed after invalidating run")
		}
	})
	t.Run("storage", func(t *testing.T) {
		j := testJournal(t)
		j.file.Close()
		if err := j.accept("email", "staff@example.test", "body"); err == nil || j.Err() == nil {
			t.Fatal("storage failure not latched")
		}
		f, err := os.CreateTemp(filepath.Dir(j.file.Name()), "replacement-*")
		if err != nil {
			t.Fatal(err)
		}
		j.file = f
		if err := j.accept("email", "staff@example.test", "body"); err == nil || len(j.Snapshot()) != 0 {
			t.Fatal("journal accepted after storage became available again")
		}
	})
}

func TestSMTPConnectionBudgetAndMixedCaseEnvelope(t *testing.T) {
	j := testJournal(t)
	sink, err := StartSMTP(j)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	connections := []net.Conn{}
	defer func() {
		for _, c := range connections {
			c.Close()
		}
	}()
	for i := 0; i < maxSMTPConnections; i++ {
		c, err := net.DialTimeout("tcp", sink.Addr(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, c)
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err = bufio.NewReader(c).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	extra, err := net.DialTimeout("tcp", sink.Addr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = bufio.NewReader(extra).ReadString('\n'); err == nil {
		t.Fatal("SMTP connection budget not enforced")
	}
	if address, err := mailpkgAddress("<staff@example.test>"); err != nil || address != "staff@example.test" {
		t.Fatal("invalid normalized envelope", address, err)
	}
	for _, bad := range []string{"staff@example.test", "<Name <staff@example.test>>", "<staff@example.test> extra"} {
		if _, err := mailpkgAddress(bad); err == nil {
			t.Fatal("accepted malformed envelope", bad)
		}
	}
}
func TestPrivateManifestExclusiveAndSymlinkRejection(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "manifest.json")
	if err := WritePrivateJSON(path, map[string]int{"version": 1}); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateJSON(path, nil); err == nil {
		t.Fatal("overwrote manifest")
	}
	if err := ReplacePrivateJSON(path, map[string]int{"version": 2}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("manifest not private")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ReplacePrivateJSON(link, nil); err == nil {
		t.Fatal("replaced symlink")
	}
}
