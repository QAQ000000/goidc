package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestHTTPWithTxWaitsForCommitAndPreservesValidationStatus(t *testing.T) {
	db, _ := testServer(t)
	s := &Server{db: db, log: slog.Default()}
	if _, err := db.Pool.Exec(t.Context(), `CREATE TABLE http_tx_probe(id int PRIMARY KEY,parent int REFERENCES http_tx_probe(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	for _, validation := range []bool{false, true} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/probe", nil).WithContext(t.Context())
		s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
			if _, err := tx.Exec(t.Context(), "INSERT INTO http_tx_probe VALUES(1,999)"); err != nil {
				return txResponse{}, err
			}
			if validation {
				return txResponse{}, &txHTTPError{422, "probe_invalid", "invalid probe"}
			}
			return txResponse{201, map[string]any{"created": true}}, nil
		})
		// Deferred FK failures keep the API's existing constraint_conflict mapping.
		want := 409
		if !validation && !strings.Contains(w.Body.String(), "constraint_conflict") {
			t.Fatal("commit error mapping lost", w.Body.String())
		}
		if validation {
			want = 422
			if !strings.Contains(w.Body.String(), "probe_invalid") {
				t.Fatal("validation error code lost", w.Body.String())
			}
		}
		if w.Code != want || strings.Contains(w.Body.String(), `"created":true`) {
			t.Fatalf("success sent before commit: %d %s", w.Code, w.Body.String())
		}
		var count int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM http_tx_probe").Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed transaction left rows: %d, %v", count, err)
		}
	}
}
