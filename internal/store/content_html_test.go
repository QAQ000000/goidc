package store

import (
	"strings"
	"testing"
)

func TestSanitizeContentHTML(t *testing.T) {
	got, err := SanitizeContentHTML(`<script>alert(1)</script><p onclick="x">Hello <strong>world</strong></p><a href="javascript:alert(1)">bad</a><a href="https://example.com/x">ok</a><img src="data:text/html,x"><img src="/api/v1/content-images/3" onerror="x">`)
	if err != nil { t.Fatal(err) }
	for _, forbidden := range []string{"script", "onclick", "javascript:", "data:text", "onerror"} { if strings.Contains(strings.ToLower(got), forbidden) { t.Fatalf("unsafe markup retained: %q", got) } }
	if !strings.Contains(got, `<strong>world</strong>`) || !strings.Contains(got, `src="/api/v1/content-images/3"`) { t.Fatalf("safe markup missing: %q", got) }
}
