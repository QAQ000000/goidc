package filestorage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type s3Config struct {
	endpoint                    *url.URL
	region, bucket, prefix      string
	accessKey, secretKey, token string
}

type s3Backend struct {
	s3Config
	client *http.Client
	now    func() time.Time
}

var s3Region = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var s3Bucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var s3AccessKey = regexp.MustCompile(`^[A-Za-z0-9_+=.@-]{1,256}$`)

func parseS3Config(values map[string]any, secrets map[string]string, requireSecrets bool) (s3Config, error) {
	var cfg s3Config
	var ok bool
	rawEndpoint, ok := values["endpoint"].(string)
	if !ok {
		return cfg, ErrConfiguration
	}
	u, err := url.Parse(rawEndpoint)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return cfg, ErrConfiguration
	}
	if u.Scheme != "https" {
		// Plain HTTP is confined to explicit loopback literals for isolated tests.
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return cfg, ErrConfiguration
		}
	}
	if strings.IndexFunc(u.Hostname(), func(r rune) bool { return r > unicode.MaxASCII || unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return cfg, ErrConfiguration
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return cfg, ErrConfiguration
		}
	}
	u.Host = strings.ToLower(u.Host)
	if u.Scheme == "https" && u.Port() == "443" {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	u.Path = ""
	cfg.endpoint = u
	cfg.region, ok = values["region"].(string)
	if !ok || !s3Region.MatchString(cfg.region) {
		return s3Config{}, ErrConfiguration
	}
	cfg.bucket, ok = values["bucket"].(string)
	if !ok || !s3Bucket.MatchString(cfg.bucket) || strings.Contains(cfg.bucket, "..") || strings.Contains(cfg.bucket, ".-") || strings.Contains(cfg.bucket, "-.") || net.ParseIP(cfg.bucket) != nil {
		return s3Config{}, ErrConfiguration
	}
	if value, exists := values["prefix"]; exists {
		cfg.prefix, ok = value.(string)
		if !ok {
			return s3Config{}, ErrConfiguration
		}
	}
	cfg.prefix = strings.Trim(cfg.prefix, "/")
	if !utf8.ValidString(cfg.prefix) || len(cfg.prefix)+1+64 > 1024 || strings.Contains(cfg.prefix, "\\") || strings.IndexFunc(cfg.prefix, unicode.IsControl) >= 0 {
		return s3Config{}, ErrConfiguration
	}
	if cfg.prefix != "" {
		for _, part := range strings.Split(cfg.prefix, "/") {
			if part == "" || part == "." || part == ".." {
				return s3Config{}, ErrConfiguration
			}
		}
	}
	if requireSecrets {
		cfg.accessKey, cfg.secretKey, cfg.token = secrets["access_key"], secrets["secret_key"], secrets["session_token"]
		if !s3AccessKey.MatchString(cfg.accessKey) || !validS3Secret(cfg.secretKey, true) || !validS3Secret(cfg.token, false) {
			return s3Config{}, ErrConfiguration
		}
	}
	return cfg, nil
}

func validS3Secret(value string, required bool) bool {
	if (required && value == "") || len(value) > 8192 {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 }) < 0
}

func newS3(values map[string]any, secrets map[string]string) (Backend, error) {
	cfg, err := parseS3Config(values, secrets, true)
	if err != nil {
		return nil, ErrConfiguration
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: -1}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		// Fresh HTTP/1 connections also prevent Transport's replay of requests
		// after a reused connection fails. No operation has an implicit retry.
		DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &s3Backend{s3Config: cfg, now: time.Now, client: &http.Client{
		Transport: transport, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (s *s3Backend) Put(ctx context.Context, key string, content []byte, expectedHash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validatePayload(content, expectedHash); err != nil {
		return err
	}
	headers := http.Header{
		"Content-Type": {"application/octet-stream"}, "If-None-Match": {"*"},
		"X-Amz-Meta-Sha256": {expectedHash},
	}
	response, err := s.request(ctx, http.MethodPut, key, content, headers)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPreconditionFailed {
		return ErrAlreadyExists
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusNoContent {
		return ErrStorage
	}
	return nil
}

func (s *s3Backend) Open(ctx context.Context, key string, size int64, hash string) (ReadSeekCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	if err := validateObject(size, hash); err != nil {
		return nil, err
	}
	response, err := s.request(ctx, http.MethodHead, key, nil, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, ErrStorage
	}
	if !validS3ObjectResponse(response, size, hash) {
		return nil, ErrIntegrity
	}
	etag := response.Header.Get("ETag")
	if !validS3ETag(etag) {
		return nil, ErrIntegrity
	}
	return &s3Reader{backend: s, ctx: ctx, key: key, size: size, hash: hash, etag: etag}, nil
}

func validS3ETag(value string) bool {
	return len(value) >= 2 && len(value) <= 256 && value[0] == '"' && value[len(value)-1] == '"' && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validS3ObjectResponse(response *http.Response, size int64, hash string) bool {
	length, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	encoding := response.Header.Get("Content-Encoding")
	return err == nil && length == size && response.ContentLength == size && response.Header.Get("X-Amz-Meta-Sha256") == hash && (encoding == "" || encoding == "identity")
}

func (s *s3Backend) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	response, err := s.request(ctx, http.MethodDelete, key, nil, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
		return ErrStorage
	}
	return nil
}

func (s *s3Backend) request(ctx context.Context, method, key string, payload []byte, headers http.Header) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	u := *s.endpoint
	u.Path = "/" + s.bucket + "/"
	if s.prefix != "" {
		u.Path += s.prefix + "/"
	}
	u.Path += key
	u.RawPath = s3EscapePath(u.Path)
	request, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, ErrStorage
	}
	for name, values := range headers {
		request.Header[name] = append([]string(nil), values...)
	}
	s.sign(request, payload, s.now().UTC())
	response, err := s.client.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, ErrStorage
	}
	return response, nil
}

func s3EscapePath(value string) string {
	const hexChars = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' || c == '/' {
			out.WriteByte(c)
		} else {
			out.WriteByte('%')
			out.WriteByte(hexChars[c>>4])
			out.WriteByte(hexChars[c&15])
		}
	}
	return out.String()
}

func s3HMAC(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}

func (s *s3Backend) sign(request *http.Request, payload []byte, now time.Time) {
	stamp, date := now.Format("20060102T150405Z"), now.Format("20060102")
	hash := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(hash[:])
	request.Header.Set("X-Amz-Date", stamp)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.token != "" {
		request.Header.Set("X-Amz-Security-Token", s.token)
	}
	names := []string{"host"}
	for name := range request.Header {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, name := range names {
		value := request.URL.Host
		if name != "host" {
			value = strings.Join(request.Header.Values(name), ",")
		}
		canonical.WriteString(name + ":" + strings.Join(strings.Fields(value), " ") + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{request.Method, request.URL.EscapedPath(), "", canonical.String(), signedHeaders, payloadHash}, "\n")
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	scope := date + "/" + s.region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	dateKey := s3HMAC([]byte("AWS4"+s.secretKey), date)
	regionKey := s3HMAC(dateKey, s.region)
	serviceKey := s3HMAC(regionKey, "s3")
	signingKey := s3HMAC(serviceKey, "aws4_request")
	signature := hex.EncodeToString(s3HMAC(signingKey, toSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}

// The reader creates a bounded, authenticated Range request only when bytes
// are requested. A failed request is sticky until an explicit Seek; it is
// never replayed automatically. The Store verifies the full content digest.
type s3Reader struct {
	mu      sync.Mutex
	backend *s3Backend
	ctx     context.Context
	key     string
	size    int64
	hash    string
	etag    string
	pos     int64
	body    io.ReadCloser
	err     error
	closed  bool
}

func (r *s3Reader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if err := r.ctx.Err(); err != nil {
		r.closeBody()
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		response, err := r.backend.request(r.ctx, http.MethodGet, r.key, nil, http.Header{
			"Range": {fmt.Sprintf("bytes=%d-%d", r.pos, r.size-1)}, "If-Match": {r.etag},
		})
		if err != nil {
			r.err = err
			return 0, err
		}
		if response.StatusCode != http.StatusPartialContent || !validS3ObjectResponse(response, r.size-r.pos, r.hash) || response.Header.Get("ETag") != r.etag || response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", r.pos, r.size-1, r.size) {
			response.Body.Close()
			r.err = ErrIntegrity
			if response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusPreconditionFailed {
				r.err = ErrStorage
				if response.StatusCode == http.StatusNotFound {
					r.err = ErrNotFound
				}
			}
			return 0, r.err
		}
		r.body = response.Body
	}
	if int64(len(p)) > r.size-r.pos {
		p = p[:r.size-r.pos]
	}
	n, err := r.body.Read(p)
	r.pos += int64(n)
	if err != nil && err != io.EOF {
		r.err = ErrStorage
		if contextErr := r.ctx.Err(); contextErr != nil {
			r.err = contextErr
		}
		r.closeBody()
		return n, r.err
	}
	if err == io.EOF && r.pos < r.size {
		r.err = ErrIntegrity
		r.closeBody()
		return n, r.err
	}
	if r.pos == r.size {
		r.closeBody()
	}
	return n, err
}

func (r *s3Reader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if err := r.ctx.Err(); err != nil {
		r.closeBody()
		return 0, err
	}
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		base = r.size
	default:
		return r.pos, ErrInvalidSeek
	}
	if offset < -base || offset > math.MaxInt64-base {
		return r.pos, ErrInvalidSeek
	}
	r.pos = base + offset
	r.closeBody()
	r.err = nil
	return r.pos, nil
}

func (r *s3Reader) closeBody() {
	if r.body != nil {
		r.body.Close()
		r.body = nil
	}
}

func (r *s3Reader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.closeBody()
	return nil
}
