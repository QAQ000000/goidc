package store

import (
	"strings"
	"testing"
)

func TestSanitizeContentHTML(t *testing.T) {
	got, err := SanitizeContentHTML(`<script>alert(1)</script><p onclick="x">Hello <strong>world</strong></p><a href="javascript:alert(1)">bad</a><a href="https://example.com/x">ok</a><img src="data:text/html,x"><img src="/api/v1/content-images/3" onerror="x">`)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"script", "onclick", "javascript:", "data:text", "onerror"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Fatalf("unsafe markup retained: %q", got)
		}
	}
	if !strings.Contains(got, `<strong>world</strong>`) || !strings.Contains(got, `src="/api/v1/content-images/3"`) {
		t.Fatalf("safe markup missing: %q", got)
	}
}

func TestContentURLsRejectBrowserEscapesAndOversizedIDs(t *testing.T) {
	for _, value := range []string{`/\evil.test`, `/%5cevil.test`, `/%2fevil.test`, "/a\x00b", "https://", "https://example.test/a b", "javascript:alert(1)"} {
		if safeContentLink(value) {
			t.Fatalf("unsafe URL accepted: %q", value)
		}
	}
	for _, value := range []string{"/news/123", "https://example.test/guide"} {
		if !safeContentLink(value) {
			t.Fatalf("valid URL rejected: %q", value)
		}
	}
	got, err := SanitizeContentHTML(`<img src="/api/v1/content-images/9999999999999999999999999">`)
	if err != nil || strings.Contains(got, "src=") {
		t.Fatal("oversized image ID retained", err)
	}
}
