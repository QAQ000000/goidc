package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMutationKeyCompatibilityAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, header, body, want string
		required, valid          bool
	}{
		{name: "required absent", required: true},
		{name: "optional absent", valid: true},
		{name: "trim", header: "  abcdefgh \t", want: "abcdefgh", required: true, valid: true},
		{name: "legacy body", body: " abcdefgh ", want: "abcdefgh", valid: true},
		{name: "same key", header: "abcdefgh", body: " abcdefgh ", want: "abcdefgh", valid: true},
		{name: "mismatch", header: "abcdefgh", body: "ijklmnop"},
		{name: "short header", header: "1234567", required: true},
		{name: "short body", body: "1234567"},
		{name: "upper bound", header: strings.Repeat("a", 128), want: strings.Repeat("a", 128), required: true, valid: true},
		{name: "too long", header: strings.Repeat("a", 129), required: true},
		{name: "byte length", body: strings.Repeat("中", 43)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", nil)
			r.Header.Set("Idempotency-Key", tc.header)
			w := httptest.NewRecorder()
			key, ok := mutationKey(w, r, tc.body, tc.required)
			if ok != tc.valid || key != tc.want {
				t.Fatalf("key=%q valid=%v", key, ok)
			}
			if !ok && (w.Code != 400 || !strings.Contains(w.Body.String(), `"invalid_idempotency_key"`)) {
				t.Fatalf("response %d: %s", w.Code, w.Body.String())
			}
		})
	}
}
