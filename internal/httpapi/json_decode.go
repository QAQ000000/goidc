package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeLimit(w, r, dst, 64<<10)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || typ != "application/json" {
		writeError(w, 415, "json_required", "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err = d.Decode(dst); err != nil {
		writeJSONDecodeError(w, err, false)
		return false
	}
	if err = d.Decode(new(any)); err != io.EOF {
		writeJSONDecodeError(w, err, true)
		return false
	}
	return true
}

func writeJSONDecodeError(w http.ResponseWriter, err error, trailing bool) {
	// Keep the existing status/code for clients that use them to recover writes.
	// Only fixed reason values are public: decoder errors can contain submitted
	// field names or values, so never return their text or field paths.
	reason, message := "read_error", "请求格式无效"
	var oversized *http.MaxBytesError
	var syntax *json.SyntaxError
	var mismatch *json.UnmarshalTypeError
	switch {
	case errors.As(err, &oversized):
		reason = "body_too_large"
	case trailing:
		reason, message = "trailing_data", "请求包含多余数据"
		if err != nil && !errors.As(err, &syntax) && !errors.Is(err, io.ErrUnexpectedEOF) {
			reason = "read_error"
		}
	case errors.Is(err, io.EOF):
		reason = "empty_body"
	case errors.As(err, &syntax), errors.Is(err, io.ErrUnexpectedEOF):
		reason = "syntax_error"
	case errors.As(err, &mismatch):
		reason = "type_mismatch"
	case err != nil && strings.HasPrefix(err.Error(), "json: unknown field "):
		reason, message = "unknown_field", "请求包含未声明字段，请检查字段名称"
	}
	writeErrorDetails(w, 400, "invalid_json", message, map[string]any{"reason": reason})
}
