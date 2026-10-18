package filestorage

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestStoragePutChecksExpectedDigestBeforeSideEffects(t *testing.T) {
	local, directory := localTestBackend(t)
	s3 := testS3Backend(t)
	requests := 0
	s3.client.Transport = storageRoundTrip(func(*http.Request) (*http.Response, error) {
		requests++
		return s3Response(http.StatusOK, nil), nil
	})
	for name, backend := range map[string]Backend{"local": local, "s3": s3} {
		t.Run(name, func(t *testing.T) {
			if err := backend.Put(context.Background(), s3TestKey(), []byte("content"), "malformed"); err != ErrInvalidObject {
				t.Fatalf("malformed digest: %v", err)
			}
			if err := backend.Put(context.Background(), s3TestKey(), []byte("content"), strings.Repeat("f", 64)); err != ErrIntegrity {
				t.Fatalf("mismatched digest: %v", err)
			}
		})
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 || requests != 0 {
		t.Fatal("failed integrity check issued storage writes")
	}
}
