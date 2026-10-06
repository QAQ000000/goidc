package fileformat

import (
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

var ticketMIMEs = map[string]bool{
	"application/gzip": true, "application/x-gzip": true, "application/json": true, "application/msword": true, "application/pdf": true,
	"application/vnd.ms-excel": true, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true, "application/zip": true,
	"application/octet-stream": true, "image/gif": true, "image/jpeg": true, "image/png": true, "image/webp": true, "text/csv": true, "text/plain": true,
}

func UnsafeTicketExtension(name string) bool {
	switch filepath.Ext(strings.ToLower(name)) {
	case ".bat", ".cmd", ".com", ".dll", ".exe", ".htm", ".html", ".js", ".php", ".pl", ".py", ".sh", ".svg":
		return true
	}
	return false
}
func TicketMIME(name, declaredHeader string, content []byte) (string, bool) {
	if UnsafeTicketExtension(name) {
		return "", false
	}
	detected := http.DetectContentType(content)
	base, _, err := mime.ParseMediaType(detected)
	if err == nil && ticketMIMEs[base] {
		return detected, true
	}
	// Active documents must not pass by claiming a harmless upload MIME.
	if base == "text/html" || base == "image/svg+xml" || base == "application/javascript" || base == "text/javascript" {
		return "", false
	}
	declared, _, err := mime.ParseMediaType(declaredHeader)
	if err == nil && ticketMIMEs[declared] {
		return declared, true
	}
	return "", false
}
