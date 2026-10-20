package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"
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
	adminImagePath := fmt.Sprintf("/api/v1/admin/content-images/%d", imageID)
	if data(t, uploaded)["url"] != imagePath {
		t.Fatal("upload must return canonical public URL")
	}
	expect(t, portalRequest(h, "GET", adminImagePath, nil, customer, ""), 401)
	expect(t, portalRequest(h, "GET", adminImagePath, nil, "", ""), 401)
	expect(t, portalRequest(h, "GET", adminImagePath, nil, outsider, ""), 403)
	for _, cookie := range []string{root, reader, root + "; " + customer, reader + "; " + customer} {
		for _, method := range []string{"GET", "HEAD"} {
			// Native image requests do not carry a portal header. Even with both
			// cookies present this fixed route must select the administrator cookie.
			w := portalRequest(h, method, adminImagePath, nil, cookie, "")
			expect(t, w, 200)
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "image/png" {
				t.Fatal("wrong image/cache headers")
			}
			if method == "GET" && !bytes.Equal(w.Body.Bytes(), file.Bytes()) {
				t.Fatal("wrong preview bytes")
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("HEAD preview included image body")
			}
		}
	}

	for _, cookie := range []string{"", customer, outsider, root, reader, root + "; " + customer} {
		expect(t, request(h, "GET", imagePath, nil, cookie, ""), 404)
	}
	expect(t, portalRequest(h, "GET", imagePath, nil, root+"; "+customer, "admin"), 404)
	expect(t, imageUpload(h, path, reader, "proof.png", "image/png", file.Bytes()), 403)
	expect(t, imageUpload(h, path, root, "proof.png", "image/jpeg", file.Bytes()), 415)
	// A valid PNG header followed by truncated pixels used to be accepted.
	expect(t, imageUpload(h, path, root, "truncated.png", "image/png", file.Bytes()[:len(file.Bytes())-20]), 415)
	expect(t, imageUpload(h, "/api/v1/admin/content/9223372036854775807/images", root, "proof.png", "image/png", file.Bytes()), 404)
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "image-reader"}, reader, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	expect(t, requestBearer(h, "GET", adminImagePath, nil, secret), 200)
	expect(t, requestBearer(h, "GET", imagePath, nil, secret), 404)
	customerKey := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "image-customer"}, customer, "")
	expect(t, customerKey, 201)
	customerSecret := data(t, customerKey)["secret"].(string)
	expect(t, requestBearer(h, "GET", adminImagePath, nil, customerSecret), 403)
	expect(t, requestBearer(h, "GET", imagePath, nil, customerSecret), 404)
	input.Status = "published"
	input.ExpectedVersion = 1
	input.BodyHTML = `<p>Safe <strong>body</strong></p><img src="` + imagePath + `" onerror="alert(1)"><a href="/\evil.test">bad</a><script>secret-script-text</script>`
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content/%d", id), input, root, ""), 200)
	expect(t, request(h, "GET", imagePath, nil, "", ""), 404)
	expect(t, request(h, "GET", imagePath, nil, customer, ""), 200)
	expect(t, portalRequest(h, "GET", imagePath, nil, root, "admin"), 404)
	expect(t, portalRequest(h, "GET", imagePath, nil, root+"; "+customerSessionCookie+"=invalid", "admin"), 404)
	expect(t, portalRequest(h, "GET", imagePath, nil, root+"; "+customer, "admin"), 200)
	// Public image URLs accept customer credentials only. Administrator API keys
	// remain restricted to the fixed management preview route.
	expect(t, requestBearer(h, "GET", imagePath, nil, secret), 404)
	expect(t, requestBearer(h, "GET", imagePath, nil, customerSecret), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", adminImagePath, nil, secret), 401)
	expect(t, requestBearer(h, "GET", imagePath, nil, secret), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(customerSecret)); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", imagePath, nil, customerSecret), 404)
	body := request(h, "GET", fmt.Sprintf("/api/v1/knowledgebase/%d", id), nil, customer, "")
	expect(t, body, 200)
	if !strings.Contains(body.Body.String(), imagePath) || strings.Contains(body.Body.String(), adminImagePath) {
		t.Fatal("article changed the canonical image URL")
	}
	for _, unsafe := range []string{"onerror", "secret-script-text", "evil.test"} {
		if strings.Contains(body.Body.String(), unsafe) {
			t.Fatal("unsafe content retained", unsafe)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(customer))); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", imagePath, nil, customer, ""), 404)
	contentRole(t, db, readerID, "products.view")
	expect(t, request(h, "GET", imagePath, nil, reader, ""), 404) // administrator cookie is anonymous on the public path
	expect(t, portalRequest(h, "GET", adminImagePath, nil, reader, ""), 403)
	input.AccessLevel = "public"
	input.ExpectedVersion = 2
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/content/%d", id), input, root, ""), 200)
	expect(t, request(h, "GET", imagePath, nil, "", ""), 200)
}

func TestContentImageAdminPreviewRechecksAuthorityAfterObjectWait(t *testing.T) {
	for _, change := range []string{"session", "permission"} {
		t.Run(change, func(t *testing.T) {
			f := localFileStorageHTTPFixture(t)
			readerID, reader := user(t, f.db, f.h, "preview-wait-reader-"+change, "admin")
			contentRole(t, f.db, readerID, "content.view")
			var file storedHTTPFile
			for _, candidate := range f.files {
				if candidate.kind == "content_images" {
					file = candidate
				}
			}
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE content_articles SET status='draft' WHERE id=$1", f.articleID); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/admin/content-images/%d", file.id)
			tx, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(t.Context(), "SELECT id FROM file_storage_objects WHERE id=$1 FOR UPDATE", file.objectID); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- portalRequest(f.h, "GET", path, nil, reader, "") }()
			waitQueryLock(t, f.db, `SELECT profile_id,location_sha256,kind,object_key,size_bytes,sha256,state FROM file_storage_objects WHERE id=$1 FOR SHARE`)
			if change == "session" {
				_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(reader)))
			} else {
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", readerID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				expect(t, out, 404)
				if bytes.Equal(out.Body.Bytes(), file.content) {
					t.Fatal("revocation exposed draft image bytes")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("preview did not finish after releasing object lock")
			}
		})
	}
}
