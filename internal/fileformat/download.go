package fileformat

import (
	"bytes"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// DownloadMIME is shared by uploads and read-only legacy file ingestion.
func DownloadMIME(name string, content []byte) (string, bool) {
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(content))
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".vcf":
		return "text/plain; charset=utf-8", detected == "text/plain"
	case ".png":
		return "image/png", detected == "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg", detected == "image/jpeg"
	case ".gif":
		return "image/gif", detected == "image/gif"
	case ".pdf":
		return "application/pdf", detected == "application/pdf"
	case ".zip", ".docx", ".xlsx", ".pptx", ".key", ".numbers", ".pages":
		return "application/octet-stream", bytes.HasPrefix(content, []byte("PK\x03\x04")) || bytes.HasPrefix(content, []byte("PK\x05\x06"))
	case ".rar":
		return "application/octet-stream", bytes.HasPrefix(content, []byte("Rar!\x1a\x07"))
	case ".doc", ".xls", ".ppt":
		return "application/octet-stream", bytes.HasPrefix(content, []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"))
	case ".rtf":
		return "application/rtf", bytes.HasPrefix(content, []byte("{\\rtf"))
	default:
		return "", false
	}
}
