package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/store"
)

func TestPairingRoutes(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	svc := app.NewService(st, app.NewEventHub(), nil)
	ts := httptest.NewServer(New(svc, Config{}, nil).Handler())
	defer ts.Close()
	anon := &http.Client{}

	resp, out := doJSON(t, anon, http.MethodPost, ts.URL+"/api/v1/computer/pairings", map[string]any{
		"hostname": "mac.local", "os": "windows",
	}, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid os status=%d", resp.StatusCode)
	}

	resp, out = doJSON(t, anon, http.MethodPost, ts.URL+"/api/v1/computer/pairings", map[string]any{
		"hostname": "mac.local", "os": "macos", "daemon_version": "2.0.0",
	}, "")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create pairing status=%d", resp.StatusCode)
	}
	pairing := out["pairing"].(map[string]any)
	id, code := pairing["id"].(string), pairing["code"].(string)
	secret := out["secret"].(string)
	if pairing["expires_at"] == nil || secret == "" {
		t.Fatal("create response missing fields")
	}

	pollURL := ts.URL + "/api/v1/computer/pairings/" + id
	resp, out = doJSON(t, anon, http.MethodGet, pollURL, nil, secret)
	if resp.StatusCode != http.StatusOK || out["status"] != "pending" {
		t.Fatalf("pending poll status=%d body=%v", resp.StatusCode, out["status"])
	}
	resp, _ = doJSON(t, anon, http.MethodGet, pollURL, nil, "wrong")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong secret status=%d", resp.StatusCode)
	}

	viewURL := ts.URL + "/api/v1/pairings/" + id + "?code=" + url.QueryEscape(code)
	resp, _ = doJSON(t, anon, http.MethodGet, viewURL, nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous view status=%d", resp.StatusCode)
	}

	jar, _ := cookiejar.New(nil)
	human := &http.Client{Jar: jar}
	doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/auth/register", map[string]any{
		"email": "lisi@sumi.dev", "password": "password123", "display_name": "Lisi",
	}, "")
	doJSON(t, human, http.MethodPost, ts.URL+"/api/v1/spaces", map[string]any{"slug": "studio", "name": "Studio"}, "")

	resp, out = doJSON(t, human, http.MethodGet, viewURL, nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("view status=%d", resp.StatusCode)
	}
	view := out["pairing"].(map[string]any)
	if view["hostname"] != "mac.local" || view["os"] != "macos" || view["status"] != "pending" || view["code"] != nil {
		t.Fatalf("view body=%v", view)
	}
	resp, _ = doJSON(t, human, http.MethodGet, ts.URL+"/api/v1/pairings/"+id+"?code=ZZZZZZ", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong code view status=%d", resp.StatusCode)
	}

	confirmURL := ts.URL + "/api/v1/computers/pairings/" + id + "/confirm"
	resp, out = doJSON(t, human, http.MethodPost, confirmURL, map[string]any{"code": code, "name": "Studio Mac"}, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm status=%d", resp.StatusCode)
	}
	compID := out["computer"].(map[string]any)["id"].(string)
	resp, _ = doJSON(t, human, http.MethodPost, confirmURL, map[string]any{"code": code}, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second confirm status=%d", resp.StatusCode)
	}

	resp, out = doJSON(t, anon, http.MethodGet, pollURL, nil, secret)
	if resp.StatusCode != http.StatusOK || out["status"] != "confirmed" || out["computer_id"] != compID || out["space_slug"] != nil {
		t.Fatalf("confirmed poll status=%d body=%v", resp.StatusCode, out)
	}
}

func TestCreatePairingRateLimitedPerIP(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ts := httptest.NewServer(New(app.NewService(st, app.NewEventHub(), nil), Config{}, nil).Handler())
	defer ts.Close()
	body := map[string]any{"hostname": "mac.local", "os": "macos"}
	for i := 0; i < pairingRequestsPerMinute; i++ {
		resp, _ := doJSON(t, &http.Client{}, http.MethodPost, ts.URL+"/api/v1/computer/pairings", body, "")
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("request %d status=%d", i, resp.StatusCode)
		}
	}
	resp, out := doJSON(t, &http.Client{}, http.MethodPost, ts.URL+"/api/v1/computer/pairings", body, "")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-limit status=%d", resp.StatusCode)
	}
	if e, _ := out["error"].(map[string]any); e["code"] != "rate_limited" {
		t.Fatalf("error = %v", out["error"])
	}
}
