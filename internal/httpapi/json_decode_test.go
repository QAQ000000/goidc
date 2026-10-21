package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONCompatibility(t *testing.T) {
	for _, contentType := range []string{"application/json", "application/json; charset=utf-8"} {
		t.Run(contentType, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(" {\"name\":\"valid\",\"count\":2} \n\t"))
			r.Header.Set("Content-Type", contentType)
			w := httptest.NewRecorder()
			var input struct {
				Name  string `json:"name"`
				Count int    `json:"count"`
			}
			if !decode(w, r, &input) || input.Name != "valid" || input.Count != 2 || w.Body.Len() != 0 {
				t.Fatalf("decode = %+v, response = %d %s", input, w.Code, w.Body.String())
			}
		})
	}
}

func TestDecodeJSONMediaType(t *testing.T) {
	for _, contentType := range []string{"", "text/plain", "application/problem+json", "application/json;invalid"} {
		t.Run(contentType, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"valid"}`))
			r.Header.Set("Content-Type", contentType)
			w := httptest.NewRecorder()
			var input struct {
				Name string `json:"name"`
			}
			if decode(w, r, &input) {
				t.Fatal("accepted a request without the required media type")
			}
			body := decodeJSONTestError(t, w)
			if w.Code != http.StatusUnsupportedMediaType || body["code"] != "json_required" || body["reason"] != nil {
				t.Fatalf("response = %d %+v", w.Code, body)
			}
		})
	}
}

func TestDecodeJSONErrorReasons(t *testing.T) {
	for _, test := range []struct {
		name, body, reason string
		limit              int64
	}{
		{name: "unknown field", body: `{"name":"valid","private-field":"private-value"}`, reason: "unknown_field"},
		{name: "nested unknown field", body: `{"nested":{"private-field":"private-value"}}`, reason: "unknown_field"},
		{name: "type mismatch", body: `{"count":"private-value"}`, reason: "type_mismatch"},
		{name: "invalid syntax", body: `{"name":private-value}`, reason: "syntax_error"},
		{name: "incomplete syntax", body: `{"name":"private-value"`, reason: "syntax_error"},
		{name: "empty", body: "", reason: "empty_body"},
		{name: "whitespace", body: "\n \t", reason: "empty_body"},
		{name: "second value", body: `{"name":"valid"} {"name":"private-value"}`, reason: "trailing_data"},
		{name: "trailing invalid syntax", body: `{"name":"valid"} private-value`, reason: "trailing_data"},
		{name: "oversized document", body: `{"name":"private-value"}`, reason: "body_too_large", limit: 12},
		{name: "oversized trailing whitespace", body: `{"name":"valid"} ` + strings.Repeat(" ", 64), reason: "body_too_large", limit: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			const correlationID = "0123456789abcdef0123456789abcdef"
			w.Header().Set("X-Request-ID", correlationID)
			var input struct {
				Name   string `json:"name"`
				Count  int    `json:"count"`
				Nested struct {
					Name string `json:"name"`
				} `json:"nested"`
			}
			limit := test.limit
			if limit == 0 {
				limit = 64 << 10
			}
			if decodeLimit(w, r, &input, limit) {
				t.Fatal("accepted an invalid JSON document")
			}
			body := decodeJSONTestError(t, w)
			if w.Code != http.StatusBadRequest || body["code"] != "invalid_json" || body["reason"] != test.reason {
				t.Fatalf("response = %d %+v", w.Code, body)
			}
			if body["correlation_id"] != correlationID {
				t.Fatalf("correlation ID lost: %+v", body)
			}
			for _, secret := range []string{"private-field", "private-value"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatalf("response disclosed submitted input: %s", w.Body.String())
				}
			}
		})
	}
}

func TestDecodeJSONExactLimit(t *testing.T) {
	const body = `{"name":"valid"}`
	for _, suffix := range []string{"", " "} {
		t.Run(suffix, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body+suffix))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			var input struct {
				Name string `json:"name"`
			}
			accepted := decodeLimit(w, r, &input, int64(len(body)))
			if accepted != (suffix == "") {
				t.Fatalf("accepted = %v, response = %s", accepted, w.Body.String())
			}
		})
	}
}

func TestDecodeJSONReadFailureDoesNotDisclose(t *testing.T) {
	for _, prefix := range []string{"", `{"name":"valid"}`} {
		t.Run(prefix, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Header.Set("Content-Type", "application/json")
			r.Body = io.NopCloser(io.MultiReader(strings.NewReader(prefix), jsonDecodeFailingReader{}))
			w := httptest.NewRecorder()
			var input map[string]any
			if decode(w, r, &input) {
				t.Fatal("accepted a failed request body")
			}
			body := decodeJSONTestError(t, w)
			if w.Code != http.StatusBadRequest || body["code"] != "invalid_json" || body["reason"] != "read_error" || strings.Contains(w.Body.String(), "private-transport-error") {
				t.Fatalf("response = %d %s", w.Code, w.Body.String())
			}
		})
	}
}

type jsonDecodeFailingReader struct{}

func (jsonDecodeFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("private-transport-error")
}

func decodeJSONTestError(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error == nil {
		t.Fatalf("invalid error response: %s (%v)", w.Body.String(), err)
	}
	return response.Error
}
