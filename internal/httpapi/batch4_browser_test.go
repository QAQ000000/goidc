package httpapi

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

func TestBatch4BrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BATCH4_READY_FILE"), os.Getenv("ZJMFGO_BATCH4_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in browser fixture")
	}
	db, h := testServer(t)
	f := setupConfigurationUpgradeFixture(t, db, h, false)
	key := []byte("01234567890123456789012345678901")
	db.PurchaseFieldEncryptionKey = key
	savePurchaseDefinitions(t, f, []store.PurchaseField{{Key: "secret", Label: "部署密钥", Type: "text", Active: true, Required: true, MaxLength: 255}})
	application := issuedApplication(t, f)
	article := articleInput("knowledge", "batch4-private-article", "draft")
	article.Title = "图文与SEO验收"
	article.AccessLevel = "login"
	articleID := contentID(t, h, f.root, article)
	readerID, _ := user(t, db, h, "batch4-reader", "admin")
	contentRole(t, db, readerID, "content.view", "products.view", "invoice_applications.view")
	var balance int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:18100")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(New(db, config.Config{PublicOrigin: "http://127.0.0.1:3100", PurchaseFieldEncryptionKey: key}, slog.Default()).Handler())
	server.Listener = listener
	server.Start()
	defer server.Close()
	var schema string
	if err := db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "schema": schema, "product_id": f.pid, "article_id": articleID, "application_id": application, "customer_id": f.uid})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	timer := time.NewTimer(30 * time.Minute)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("batch4 browser fixture timed out")
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				goto verify
			}
		}
	}
verify:
	var orders, encrypted, documents, adjustments, images int
	var afterBalance int64
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders),
 (SELECT count(*) FROM order_items WHERE purchase_fields @> '[{"key":"secret","encrypted":true}]'::jsonb),
 (SELECT count(*) FROM invoice_application_documents),(SELECT count(*) FROM invoice_application_adjustments),
 (SELECT count(*) FROM content_images),(SELECT balance_cents FROM users WHERE id=$1)`, f.uid).Scan(&orders, &encrypted, &documents, &adjustments, &images, &afterBalance); err != nil {
		t.Fatal(err)
	}
	if orders != 2 || encrypted != 1 || documents != 2 || adjustments != 1 || images != 1 || balance != afterBalance {
		t.Fatalf("incomplete acceptance orders=%d encrypted=%d documents=%d adjustments=%d images=%d balance_changed=%t", orders, encrypted, documents, adjustments, images, balance != afterBalance)
	}
	var valid bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT status='published' AND access_level='login' AND seo_title='图文SEO验收标题' AND body_html LIKE '%<strong>安全图文</strong>%' AND body_html NOT LIKE '%onerror%' FROM content_articles WHERE id=$1", articleID).Scan(&valid); err != nil || !valid {
		t.Fatal("content acceptance incomplete", err)
	}
	t.Log("combined browser acceptance: encrypted checkout, two invoice files, one red record, private rich image, SEO; balance unchanged")
}
