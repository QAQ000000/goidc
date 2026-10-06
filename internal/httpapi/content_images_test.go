package httpapi

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
)

func imageUpload(h http.Handler, path, cookie, filename, mimeType string, content []byte) *httptest.ResponseRecorder {
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		panic(err)
	}
	part.Write(content)
	writer.Close()
	r := httptest.NewRequest("POST", path, &payload)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://localhost:3088")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestContentImagesDecodePermissionsAndVisibility(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "image-root", "super_admin")
	readerID, reader := user(t, db, h, "image-reader", "admin")
	contentRole(t, db, readerID, "content.view")
	outsiderID, outsider := user(t, db, h, "image-outsider", "admin")
	contentRole(t, db, outsiderID, "products.view")
	_, customer := user(t, db, h, "image-customer", "customer")
	input := articleInput("knowledge", "image-private", "draft")
	input.AccessLevel = "login"
	id := contentID(t, h, root, input)
	path := fmt.Sprintf("/api/v1/admin/content/%d/images", id)
	var file bytes.Buffer
	if err := png.Encode(&file, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	uploaded := imageUpload(h, path, root, "proof.png", "image/png", file.Bytes())
	expect(t, uploaded, 201)
	imageID := int64(data(t, uploaded)["id"].(float64))
	imagePath := fmt.Sprintf("/api/v1/content-images/%d", imageID)
	for _, cookie := range []string{root, reader} {
		w := request(h, "GET", imagePath, nil, cookie, "")
		expect(t, w, 200)
		if !bytes.Equal(w.Body.Bytes(), file.Bytes()) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("wrong image or cache headers")
		}
	}
	for _, cookie := range []string{"", customer, outsider} {
		expect(t, request(h, "GET", imagePath, nil, cookie, ""), 404)
	}
	expect(t, imageUpload(h, path, reader, "proof.png", "image/png", file.Bytes()), 403)
	expect(t, imageUpload(h, path, root, "proof.png", "image/jpeg", file.Bytes()), 415)
	// A valid PNG header followed by truncated pixels used to be accepted.
	expect(t, imageUpload(h, path, root, "truncated.png", "image/png", file.Bytes()[:len(file.Bytes())-20]), 415)
	expect(t, imageUpload(h, "/api/v1/admin/content/9223372036854775807/images", root, "proof.png", "image/png", file.Bytes()), 404)
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "image-reader"}, reader, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", imagePath, nil, secret), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", imagePath, nil, secret), 404)
	input.Status = "published"
	input.ExpectedVersion = 1
	input.BodyHTML = `<p>Safe <strong>body</strong></p><img src="` + imagePath + `" onerror="alert(1)"><a href="/\evil.test">bad</a><script>secret-script-text</script>`
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content/%d", id), input, root, ""), 200)
	expect(t, request(h, "GET", imagePath, nil, "", ""), 404)
	expect(t, request(h, "GET", imagePath, nil, customer, ""), 200)
	body := request(h, "GET", fmt.Sprintf("/api/v1/knowledgebase/%d", id), nil, customer, "")
	expect(t, body, 200)
	for _, unsafe := range []string{"onerror", "secret-script-text", "evil.test"} {
		if strings.Contains(body.Body.String(), unsafe) {
			t.Fatal("unsafe content retained", unsafe)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(strings.TrimPrefix(customer, "zjmfgo_session="))); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", imagePath, nil, customer, ""), 404)
	contentRole(t, db, readerID, "products.view")
	expect(t, request(h, "GET", imagePath, nil, reader, ""), 200) // logged in published knowledge
	input.AccessLevel = "public"
	input.ExpectedVersion = 2
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content/%d", id), input, root, ""), 200)
	expect(t, request(h, "GET", imagePath, nil, "", ""), 200)
}
