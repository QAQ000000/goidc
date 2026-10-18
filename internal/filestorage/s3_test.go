package filestorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type storageRoundTrip func(*http.Request) (*http.Response, error)

func (f storageRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func s3TestHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func s3TestKey() string { return strings.Repeat("a", 64) }

func testS3Backend(t *testing.T) *s3Backend {
	t.Helper()
	profile := s3TestProfile()
	backend, err := newS3(profile.Values, profile.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	result := backend.(*s3Backend)
	result.now = func() time.Time { return time.Date(2026, 10, 18, 0, 1, 2, 0, time.UTC) }
	return result
}

func s3Response(code int, data []byte) *http.Response {
	return &http.Response{StatusCode: code, ContentLength: int64(len(data)), Header: http.Header{
		"Content-Length": {strconv.Itoa(len(data))},
	}, Body: io.NopCloser(bytes.NewReader(data))}
}

func s3HeadResponse(data []byte) *http.Response {
	response := s3Response(http.StatusOK, nil)
	response.ContentLength = int64(len(data))
	response.Header.Set("Content-Length", strconv.Itoa(len(data)))
	response.Header.Set("X-Amz-Meta-Sha256", s3TestHash(data))
	response.Header.Set("ETag", `"opaque-version-etag"`)
	return response
}

func s3RangeResponse(data []byte, offset int) *http.Response {
	response := s3Response(http.StatusPartialContent, data[offset:])
	response.Header.Set("X-Amz-Meta-Sha256", s3TestHash(data))
	response.Header.Set("ETag", `"opaque-version-etag"`)
	response.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(data)-1, len(data)))
	return response
}

func TestS3SignatureMatchesPublishedAWSGetObjectVector(t *testing.T) {
	backend := &s3Backend{s3Config: s3Config{
		region: "us-east-1", accessKey: "AKIAIOSFODNN7EXAMPLE", secretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}}
	request, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-9")
	backend.sign(request, nil, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if request.Header.Get("Authorization") != want {
		t.Fatal("SigV4 diverged from the published S3 GET Object signing vector")
	}
}

func TestS3PrivatePathStyleConditionalPutHeadRangeSeekDelete(t *testing.T) {
	backend := testS3Backend(t)
	backend.prefix = "team+one/文件"
	key := s3TestKey()
	content := []byte("0123456789-abcdefghijklmnopqrstuvwxyz")
	var stored []byte
	var requests []string
	backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "s3.example.invalid" || request.URL.Path != "/private-files/team+one/文件/"+key || !strings.Contains(request.URL.EscapedPath(), "team%2Bone/%E6%96%87%E4%BB%B6/") || request.URL.RawQuery != "" {
			t.Fatal("request is not the canonical private path-style object URL")
		}
		if request.Header.Get("X-Amz-Security-Token") != backend.token || !strings.Contains(request.Header.Get("Authorization"), "x-amz-security-token") || request.Header.Get("X-Amz-Date") != "20261018T000102Z" {
			t.Fatal("request did not sign named temporary credentials")
		}
		requests = append(requests, request.Method+":"+request.Header.Get("Range"))
		switch request.Method {
		case http.MethodPut:
			if request.Header.Get("If-None-Match") != "*" || request.Header.Get("X-Amz-Acl") != "" || request.Header.Get("Content-Type") != "application/octet-stream" || !strings.Contains(request.Header.Get("Authorization"), "if-none-match") {
				t.Fatal("PUT lost exclusive private-object semantics")
			}
			body, err := io.ReadAll(request.Body)
			if err != nil || request.Header.Get("X-Amz-Meta-Sha256") != s3TestHash(body) || request.Header.Get("X-Amz-Content-Sha256") != s3TestHash(body) {
				t.Fatal("PUT digest does not describe the transmitted bytes")
			}
			if stored != nil {
				return s3Response(http.StatusPreconditionFailed, nil), nil
			}
			stored = body
			return s3Response(http.StatusOK, nil), nil
		case http.MethodHead:
			if stored == nil {
				return s3Response(http.StatusNotFound, nil), nil
			}
			return s3HeadResponse(stored), nil
		case http.MethodGet:
			if request.Header.Get("If-Match") != `"opaque-version-etag"` || !strings.Contains(request.Header.Get("Authorization"), "if-match;range") {
				t.Fatal("range was not signed against the HEAD object version")
			}
			var start, end int
			if _, err := fmt.Sscanf(request.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil || end != len(stored)-1 || start < 0 || start >= len(stored) {
				t.Fatal("invalid bounded range")
			}
			return s3RangeResponse(stored, start), nil
		case http.MethodDelete:
			stored = nil
			return s3Response(http.StatusNoContent, nil), nil
		}
		t.Fatal("unexpected protocol operation")
		return nil, errors.New("unreachable")
	})
	ctx := context.Background()
	if err := backend.Put(ctx, key, content, s3TestHash(content)); err != nil {
		t.Fatal(err)
	}
	if err := backend.Put(ctx, key, []byte("overwrite"), s3TestHash([]byte("overwrite"))); err != ErrAlreadyExists || !bytes.Equal(stored, content) {
		t.Fatal("conditional write overwrote an existing object")
	}
	reader, err := backend.Open(ctx, key, int64(len(content)), s3TestHash(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || requests[2] != "HEAD:" {
		t.Fatal("Open must validate metadata without buffering a GET")
	}
	first := make([]byte, 4)
	if _, err := io.ReadFull(reader, first); err != nil || string(first) != "0123" {
		t.Fatal("streamed first range is invalid")
	}
	if pos, err := reader.Seek(-3, io.SeekEnd); err != nil || pos != int64(len(content)-3) {
		t.Fatal("SeekEnd failed")
	}
	tail, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(tail, content[len(content)-3:]) {
		t.Fatal("seeked tail is invalid")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(all, content) {
		t.Fatal("full streamed range is invalid")
	}
	if _, err := reader.Seek(2, io.SeekCurrent); err != nil {
		t.Fatal(err)
	}
	requestsBeforeEOF := len(requests)
	if n, err := reader.Read(first); n != 0 || err != io.EOF || len(requests) != requestsBeforeEOF {
		t.Fatal("seeking past EOF must not issue an invalid range")
	}
	if _, err := reader.Seek(-1, io.SeekStart); err != ErrInvalidSeek {
		t.Fatal("negative seek was accepted")
	}
	if _, err := reader.Seek(0, 99); err != ErrInvalidSeek {
		t.Fatal("unknown seek origin was accepted")
	}
	if _, err := reader.Seek(math.MaxInt64, io.SeekCurrent); err != ErrInvalidSeek {
		t.Fatal("overflowing seek was accepted")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(first); err != io.ErrClosedPipe {
		t.Fatal("closed stream remained readable")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != io.ErrClosedPipe {
		t.Fatal("closed stream remained seekable")
	}
	if err := backend.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := backend.Delete(ctx, key); err != nil {
		t.Fatal("delete must be idempotent")
	}
	if _, err := backend.Open(ctx, key, int64(len(content)), s3TestHash(content)); err != ErrNotFound {
		t.Fatal("deleted object is still available")
	}
}

func TestS3RejectsUntrustedHeadMetadata(t *testing.T) {
	content := []byte("content")
	cases := map[string]func(*http.Response){
		"missing size":        func(r *http.Response) { r.Header.Del("Content-Length") },
		"wrong size":          func(r *http.Response) { r.Header.Set("Content-Length", "8"); r.ContentLength = 8 },
		"missing digest":      func(r *http.Response) { r.Header.Del("X-Amz-Meta-Sha256") },
		"wrong digest":        func(r *http.Response) { r.Header.Set("X-Amz-Meta-Sha256", strings.Repeat("f", 64)) },
		"missing version":     func(r *http.Response) { r.Header.Del("ETag") },
		"weak version":        func(r *http.Response) { r.Header.Set("ETag", `W/"weak"`) },
		"transformed content": func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			backend := testS3Backend(t)
			backend.client.Transport = storageRoundTrip(func(*http.Request) (*http.Response, error) {
				response := s3HeadResponse(content)
				mutate(response)
				return response, nil
			})
			if _, err := backend.Open(context.Background(), s3TestKey(), int64(len(content)), s3TestHash(content)); err != ErrIntegrity {
				t.Fatalf("untrusted metadata did not fail closed: %v", err)
			}
		})
	}
}

func TestS3RangeProtocolFailuresAreStickyAndSanitized(t *testing.T) {
	content := []byte("content")
	cases := map[string]func(*http.Response){
		"ignored range":       func(r *http.Response) { r.StatusCode = http.StatusOK },
		"wrong interval":      func(r *http.Response) { r.Header.Set("Content-Range", "bytes 1-6/7") },
		"wrong length":        func(r *http.Response) { r.Header.Set("Content-Length", "8"); r.ContentLength = 8 },
		"wrong hash":          func(r *http.Response) { r.Header.Del("X-Amz-Meta-Sha256") },
		"changed version":     func(r *http.Response) { r.Header.Set("ETag", `"new-version"`) },
		"precondition failed": func(r *http.Response) { r.StatusCode = http.StatusPreconditionFailed },
		"truncated body":      func(r *http.Response) { r.Body = io.NopCloser(strings.NewReader("short")) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			backend := testS3Backend(t)
			gets := 0
			backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodHead {
					return s3HeadResponse(content), nil
				}
				gets++
				response := s3RangeResponse(content, 0)
				mutate(response)
				return response, nil
			})
			reader, err := backend.Open(context.Background(), s3TestKey(), int64(len(content)), s3TestHash(content))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := io.ReadAll(reader); err != ErrIntegrity && err != ErrStorage {
				t.Fatalf("invalid range protocol was accepted: %v", err)
			}
			if _, err := reader.Read(make([]byte, 1)); err == nil || gets != 1 {
				t.Fatal("failed range was automatically retried")
			}
		})
	}
}

func TestS3TransportFailuresRedirectsAndStatusesDoNotLeakOrRetry(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308, 400, 401, 403, 405, 409, 429, 500, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			backend := testS3Backend(t)
			calls := 0
			backend.client.Transport = storageRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				response := s3Response(status, []byte("response-with-"+backend.secretKey+backend.token))
				response.Header.Set("Location", "https://credential-sink.example.invalid")
				return response, nil
			})
			for _, operation := range []func() error{
				func() error {
					return backend.Put(context.Background(), s3TestKey(), []byte("content"), s3TestHash([]byte("content")))
				},
				func() error {
					_, err := backend.Open(context.Background(), s3TestKey(), 7, s3TestHash([]byte("content")))
					return err
				},
				func() error { return backend.Delete(context.Background(), s3TestKey()) },
			} {
				before := calls
				if err := operation(); err != ErrStorage || calls != before+1 {
					t.Fatalf("unsafe status handling: %v, requests %d", err, calls-before)
				}
			}
		})
	}
	backend := testS3Backend(t)
	calls := 0
	backend.client.Transport = storageRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("transport failure with %s %s %s", backend.accessKey, backend.secretKey, backend.token)
	})
	if err := backend.Put(context.Background(), s3TestKey(), []byte("content"), s3TestHash([]byte("content"))); err != ErrStorage || calls != 1 {
		t.Fatal("transport error was leaked or write was retried")
	}
	transport := testS3Backend(t).client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives || !transport.DisableCompression || transport.ForceAttemptHTTP2 || transport.TLSNextProto == nil {
		t.Fatal("S3 transport inherited ambient proxy, transparent decoding or connection retries")
	}
}

type failingS3Body struct {
	err error
}

func (b failingS3Body) Read([]byte) (int, error) { return 0, b.err }
func (b failingS3Body) Close() error             { return nil }

func TestS3StreamErrorsDoNotLeakOrAutomaticallyRetry(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(strconv.FormatBool(transportFailure), func(t *testing.T) {
			backend := testS3Backend(t)
			content := []byte("content")
			gets := 0
			backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodHead {
					return s3HeadResponse(content), nil
				}
				gets++
				unsafe := fmt.Errorf("external stream error %s %s", backend.secretKey, backend.token)
				if transportFailure {
					return nil, unsafe
				}
				response := s3RangeResponse(content, 0)
				response.Body = failingS3Body{err: unsafe}
				return response, nil
			})
			reader, err := backend.Open(context.Background(), s3TestKey(), int64(len(content)), s3TestHash(content))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			if _, err := io.ReadAll(reader); err != ErrStorage {
				t.Fatal("raw streaming error escaped the adapter")
			}
			if _, err := reader.Read(make([]byte, 1)); err != ErrStorage || gets != 1 {
				t.Fatal("failed streaming request was retried")
			}
		})
	}
}

func TestS3RejectsInvalidKeysAndMetadataBeforeNetwork(t *testing.T) {
	backend := testS3Backend(t)
	calls := 0
	backend.client.Transport = storageRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("network must not be reached")
	})
	for _, key := range []string{"", "../object", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("0", 65), strings.Repeat("a", 63) + "/"} {
		if err := backend.Put(context.Background(), key, nil, ""); err != ErrInvalidKey {
			t.Fatal("invalid PUT key accepted")
		}
		if _, err := backend.Open(context.Background(), key, 0, s3TestHash(nil)); err != ErrInvalidKey {
			t.Fatal("invalid GET key accepted")
		}
		if err := backend.Delete(context.Background(), key); err != ErrInvalidKey {
			t.Fatal("invalid DELETE key accepted")
		}
	}
	if _, err := backend.Open(context.Background(), s3TestKey(), -1, s3TestHash(nil)); err != ErrInvalidObject {
		t.Fatal("negative object length accepted")
	}
	if _, err := backend.Open(context.Background(), s3TestKey(), 0, "not-a-hash"); err != ErrInvalidObject {
		t.Fatal("invalid expected digest accepted")
	}
	if err := backend.Put(context.Background(), s3TestKey(), nil, "text/plain\r\nAuthorization: injected"); err != ErrInvalidObject {
		t.Fatal("malformed expected digest accepted")
	}
	if calls != 0 {
		t.Fatal("invalid input reached the network")
	}
}

func TestS3ContextCancellationBeforeDuringAndAfterOpen(t *testing.T) {
	backend := testS3Backend(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	backend.client.Transport = storageRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unreachable") })
	if err := backend.Put(ctx, s3TestKey(), nil, s3TestHash(nil)); err != context.Canceled {
		t.Fatal("pre-request cancellation lost")
	}
	if _, err := backend.Open(ctx, s3TestKey(), 0, s3TestHash(nil)); err != context.Canceled {
		t.Fatal("HEAD cancellation lost")
	}
	if err := backend.Delete(ctx, s3TestKey()); err != context.Canceled || calls != 0 {
		t.Fatal("cancelled request reached the transport")
	}
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	done := make(chan error, 1)
	go func() { done <- backend.Put(ctx, s3TestKey(), nil, s3TestHash(nil)) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal("in-flight cancellation was replaced with an unsafe transport error")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight cancellation did not finish")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	content := []byte("content")
	backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodHead {
			return s3HeadResponse(content), nil
		}
		return s3RangeResponse(content, 0), nil
	})
	reader, err := backend.Open(ctx, s3TestKey(), int64(len(content)), s3TestHash(content))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := reader.Read(make([]byte, 1)); err != context.Canceled {
		t.Fatal("stream cancellation lost")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != context.Canceled {
		t.Fatal("cancelled stream remained seekable")
	}
}

type observedBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *observedBody) Close() error { b.mu.Lock(); defer b.mu.Unlock(); b.closed = true; return nil }

func TestS3ResponseBodiesCloseOnFailureSeekEOFAndClose(t *testing.T) {
	backend := testS3Backend(t)
	content := []byte("content")
	var bodies []*observedBody
	backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
		response := s3HeadResponse(content)
		if request.Method == http.MethodGet {
			response = s3RangeResponse(content, 0)
		}
		body := &observedBody{Reader: response.Body}
		response.Body = body
		bodies = append(bodies, body)
		return response, nil
	})
	reader, err := backend.Open(context.Background(), s3TestKey(), int64(len(content)), s3TestHash(content))
	if err != nil {
		t.Fatal(err)
	}
	if !bodies[0].closed {
		t.Fatal("HEAD leaked its response body")
	}
	reader.Read(make([]byte, 1))
	reader.Seek(0, io.SeekStart)
	if !bodies[1].closed {
		t.Fatal("Seek leaked the old range body")
	}
	io.ReadAll(reader)
	if !bodies[2].closed {
		t.Fatal("EOF leaked the range body")
	}
	reader.Seek(0, io.SeekStart)
	reader.Read(make([]byte, 1))
	reader.Close()
	for _, body := range bodies {
		if !body.closed {
			t.Fatal("object close leaked a response body")
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal("stream close must be idempotent")
	}
}

func TestS3ZeroLengthObjectUsesHeadAndNoInvalidRange(t *testing.T) {
	backend := testS3Backend(t)
	calls := 0
	backend.client.Transport = storageRoundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodHead {
			t.Fatal("zero-byte object issued an invalid range")
		}
		return s3HeadResponse(nil), nil
	})
	reader, err := backend.Open(context.Background(), s3TestKey(), 0, s3TestHash(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if result, err := io.ReadAll(reader); err != nil || len(result) != 0 || calls != 1 {
		t.Fatal("empty object did not round-trip")
	}
}
