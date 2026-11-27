package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"testing"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/store"
)

var testPNGBytes = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
	0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
	0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
	0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
	0x42, 0x60, 0x82,
}

func putAvatarFile(t *testing.T, client *http.Client, url, filename, contentType string, data []byte) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatalf("create multipart part: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write multipart part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("put avatar: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestSpaceAvatarHTTPUploadServeAndDelete(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := New(svc, Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ownerJar, _ := cookiejar.New(nil)
	owner := &http.Client{Jar: ownerJar}
	if resp, _ := doJSON(t, owner, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "owner@sumi.dev", "password": "secret1234", "display_name": "Lisi",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register owner: %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, owner, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio", "name": "Sumi Studio",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space: %d", resp.StatusCode)
	}

	outsiderJar, _ := cookiejar.New(nil)
	outsider := &http.Client{Jar: outsiderJar}
	if resp, _ := doJSON(t, outsider, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "out@sumi.dev", "password": "secret1234", "display_name": "Mallory",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register outsider: %d", resp.StatusCode)
	}

	avatarURL := ts.URL + "/api/v1/spaces/studio/avatar"

	// Reject SVG even if client claims image/png.
	if resp, _ := putAvatarFile(t, owner, avatarURL, "logo.png", "image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("svg upload: want 400, got %d", resp.StatusCode)
	}

	// Upload valid PNG.
	resp, body := putAvatarFile(t, owner, avatarURL, "logo.png", "image/png", testPNGBytes)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("png upload: want 200, got %d body=%v", resp.StatusCode, body)
	}
	spaceObj := body["space"].(map[string]any)
	hash, _ := spaceObj["avatar_hash"].(string)
	if len(hash) != 64 {
		t.Fatalf("expected 64-char avatar_hash, got %q", hash)
	}

	// Outsider cannot read the Space's avatar.
	outResp, err := outsider.Get(avatarURL + "?v=" + hash)
	if err != nil {
		t.Fatalf("outsider get avatar: %v", err)
	}
	_ = outResp.Body.Close()
	if outResp.StatusCode != http.StatusForbidden {
		t.Fatalf("outsider get avatar: want 403, got %d", outResp.StatusCode)
	}

	// Owner fetches avatar with immutable caching and ETag revalidation.
	getResp, err := owner.Get(avatarURL + "?v=" + hash)
	if err != nil {
		t.Fatalf("owner get avatar: %v", err)
	}
	raw, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get avatar: want 200, got %d", getResp.StatusCode)
	}
	if getResp.Header.Get("Content-Type") != "image/png" || getResp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("unexpected headers: ct=%q nosniff=%q", getResp.Header.Get("Content-Type"), getResp.Header.Get("X-Content-Type-Options"))
	}
	if getResp.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatalf("unexpected Cache-Control: %q", getResp.Header.Get("Cache-Control"))
	}
	if !bytes.Equal(raw, testPNGBytes) {
		t.Fatalf("avatar bytes mismatch")
	}

	req304, _ := http.NewRequest(http.MethodGet, avatarURL+"?v="+hash, nil)
	req304.Header.Set("If-None-Match", `"`+hash+`"`)
	resp304, err := owner.Do(req304)
	if err != nil {
		t.Fatalf("etag get: %v", err)
	}
	_ = resp304.Body.Close()
	if resp304.StatusCode != http.StatusNotModified {
		t.Fatalf("etag get: want 304, got %d", resp304.StatusCode)
	}

	// Delete removes the avatar and subsequent GET returns 404.
	delResp, delBody := doJSON(t, owner, http.MethodDelete, avatarURL, nil, "")
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete avatar: want 200, got %d body=%v", delResp.StatusCode, delBody)
	}
	afterResp, err := owner.Get(avatarURL)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	_ = afterResp.Body.Close()
	if afterResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", afterResp.StatusCode)
	}
}
