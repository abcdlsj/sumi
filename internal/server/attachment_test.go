package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/store"
)

func uploadFile(t *testing.T, client *http.Client, url, name, content string) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	header.Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
	part, err := mw.CreatePart(header)
	if err != nil {
		t.Fatalf("create form part: %v", err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatalf("write form part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestAttachmentHTTPUploadBindAndDownload(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := New(svc, Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	human := &http.Client{Jar: jar}

	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "attach@sumi.dev", "password": "password123", "display_name": "Lisi",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio", "name": "Sumi Studio",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space status=%d", resp.StatusCode)
	}

	bootResp, boot := doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/spaces/studio/bootstrap", nil, "")
	if bootResp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap status=%d", bootResp.StatusCode)
	}
	convID := boot["conversations"].([]any)[0].(map[string]any)["id"].(string)

	resp, upOut := uploadFile(t, human, ts.URL+"/api/v1/spaces/studio/attachments", "report.pdf", "pdf-bytes")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status=%d body=%+v", resp.StatusCode, upOut)
	}
	attID := upOut["attachment"].(map[string]any)["id"].(string)

	resp, msgOut := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces/studio/conversations/"+convID+"/messages", map[string]any{
		"body_markdown":  "see attached",
		"attachment_ids": []string{attID},
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("send status=%d body=%+v", resp.StatusCode, msgOut)
	}
	attachments := msgOut["message"].(map[string]any)["attachments"].([]any)
	if len(attachments) != 1 || attachments[0].(map[string]any)["id"] != attID {
		t.Fatalf("message did not carry the attachment: %+v", attachments)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/spaces/studio/attachments/"+attID, nil)
	dl, err := human.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download status=%d", dl.StatusCode)
	}
	body, _ := io.ReadAll(dl.Body)
	if string(body) != "pdf-bytes" {
		t.Fatalf("download body mismatch: %q", body)
	}
	if ct := dl.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("unexpected content type %q", ct)
	}
	if cd := dl.Header.Get("Content-Disposition"); !strings.Contains(cd, "report.pdf") {
		t.Fatalf("unexpected content disposition %q", cd)
	}
	if dl.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("attachment responses must be sniff-proof")
	}

	// Unauthenticated clients must not read attachments.
	anon := &http.Client{}
	anonResp, err := anon.Get(ts.URL + "/api/v1/spaces/studio/attachments/" + attID)
	if err != nil {
		t.Fatalf("anon download: %v", err)
	}
	defer anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous download, got %d", anonResp.StatusCode)
	}
}

func TestAttachmentUploadRejectsMissingFile(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := New(svc, Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	human := &http.Client{Jar: jar}
	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "nofile@sumi.dev", "password": "password123", "display_name": "Lisi",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio", "name": "Sumi Studio",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space status=%d", resp.StatusCode)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/api/v1/spaces/studio/attachments", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := human.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAttachmentInfersSafeImageMediaTypeFromOctetStream(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := New(svc, Config{}, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	jar, _ := cookiejar.New(nil)
	human := &http.Client{Jar: jar}
	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "img@sumi.dev", "password": "password123", "display_name": "Lisi",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{
		"slug": "studio", "name": "Sumi Studio",
	}, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create space status=%d", resp.StatusCode)
	}

	uploadOctetStream := func(name, content string) map[string]any {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		_, _ = io.WriteString(part, content)
		_ = mw.Close()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/spaces/studio/attachments", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := human.Do(req)
		if err != nil {
			t.Fatalf("upload %s: %v", name, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload %s status=%d", name, resp.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out["attachment"].(map[string]any)
	}

	pngAtt := uploadOctetStream("chart.png", "png-bytes")
	if pngAtt["media_type"] != "image/png" {
		t.Fatalf("expected inferred image/png, got %v", pngAtt["media_type"])
	}
	dl, err := human.Get(ts.URL + "/api/v1/spaces/studio/attachments/" + pngAtt["id"].(string))
	if err != nil {
		t.Fatalf("download png: %v", err)
	}
	defer dl.Body.Close()
	if ct := dl.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("expected served Content-Type image/png, got %q", ct)
	}

	// Active web types (like HTML) must never be promoted from application/octet-stream.
	htmlAtt := uploadOctetStream("page.html", "<script>alert(1)</script>")
	if htmlAtt["media_type"] != "application/octet-stream" {
		t.Fatalf("expected html upload to stay application/octet-stream, got %v", htmlAtt["media_type"])
	}
}
