package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/httpapi"
	"golang.org/x/crypto/bcrypt"
)

func TestImportedTicketBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_TICKET_BROWSER_READY"), os.Getenv("ZJMFGO_TICKET_BROWSER_FINISH")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	if ready == finish {
		t.Fatal("distinct fixture paths required")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("fresh fixture paths required")
		}
	}
	db := importTestDB(t)
	admin := navigationAdmin(t, db)
	s, root := ticketImportFixture(t)
	if err := fileImporter(s, root, admin).apply(t.Context(), db.Pool); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	password := "fixture-" + hex.EncodeToString(raw)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	uid, other := importedID(t, db, "users", 10), importedID(t, db, "users", 11)
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET password_hash=$1,password_reset_required=false,status='active' WHERE id=ANY($2)", string(hash), []int64{uid, other, admin}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.New(db, config.Config{PublicOrigin: "http://localhost:3089"}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	var private, public int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM ticket_attachments WHERE note_id IS NOT NULL").Scan(&private); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM ticket_attachments WHERE filename='客户.txt'").Scan(&public); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"api_url": server.URL, "origin": "http://localhost:3089", "admin": "nav-import-root", "customer": "legacy-10", "foreign": "legacy-11", "password": password, "ticket_id": importedID(t, db, "tickets", 201), "department_id": importedID(t, db, "ticket_departments", 71), "private_attachment_id": private, "public_attachment_id": public})
	file, err := os.OpenFile(ready, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	if _, err = file.Write(payload); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated imported-ticket browser fixture ready")
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		case <-tick.C:
			if _, err = os.Stat(finish); err == nil {
				os.Remove(finish)
				return
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
	}
}
