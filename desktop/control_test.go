package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestControlServerSecurityAndSwitching(t *testing.T) {
	rt := newRuntime(Config{
		ServerURL:     defaultServerURL,
		ComputerToken: "secret-computer-token",
		DataDir:       t.TempDir(),
	}, slog.New(slog.DiscardHandler))
	rt.healthCheck = func(_ context.Context, u string) bool {
		return u == defaultServerURL || u == "https://sumi.team"
	}
	rt.stopComputer = func(context.Context, string) error { return nil }
	defer rt.detach()

	var cs *controlServer
	onSwitch := func(ctx context.Context, rawURL string) (desktopServerState, error) {
		if _, err := rt.switchServer(ctx, rawURL); err != nil {
			return desktopServerState{}, err
		}
		return cs.currentState(), nil
	}

	var opened []string
	onOpenURL := func(rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}

	var hideCalls int
	onHideWindow := func() error {
		hideCalls++
		return nil
	}

	var err error
	cs, err = startControlServer(rt, onSwitch, onOpenURL, onHideWindow, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("start control server: %v", err)
	}
	defer cs.shutdown()

	// Fragment must carry bridge coordinates client-side, never in the HTTP query string.
	bridged := cs.attachBridge("https://sumi.team/?shell=desktop")
	parsed, err := url.Parse(bridged)
	if err != nil {
		t.Fatalf("parse bridged url: %v", err)
	}
	if strings.Contains(parsed.RawQuery, "desktop_token") {
		t.Fatal("desktop control token must never appear in URL query string")
	}
	frag, err := url.ParseQuery(parsed.Fragment)
	if err != nil || frag.Get("desktop_token") != cs.token || frag.Get("desktop_control") != cs.baseURL {
		t.Fatalf("unexpected bridge fragment: %q", parsed.Fragment)
	}

	// Wrong origin must be rejected with 403.
	req, _ := http.NewRequest(http.MethodGet, cs.baseURL+"/desktop/server", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer "+cs.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request wrong origin: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign origin, got %d", resp.StatusCode)
	}

	// Missing or invalid token must be rejected with 401.
	req, _ = http.NewRequest(http.MethodGet, cs.baseURL+"/desktop/server", nil)
	req.Header.Set("Origin", defaultServerURL)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request bad token: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad token, got %d", resp.StatusCode)
	}

	// Valid GET returns current state and never exposes computer_token.
	req, _ = http.NewRequest(http.MethodGet, cs.baseURL+"/desktop/server", nil)
	req.Header.Set("Origin", defaultServerURL)
	req.Header.Set("Authorization", "Bearer "+cs.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("valid GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("secret-computer-token")) {
		t.Fatal("control response must never contain computer_token")
	}
	var state desktopServerState
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if !state.IsLocal || state.ServerURL != defaultServerURL {
		t.Fatalf("unexpected state: %+v", state)
	}

	// POST to unreachable server returns 502.
	req, _ = http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/server", bytes.NewBufferString(`{"server_url":"https://dead.team"}`))
	req.Header.Set("Origin", defaultServerURL)
	req.Header.Set("Authorization", "Bearer "+cs.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST unreachable: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for unreachable server, got %d", resp.StatusCode)
	}

	// POST to reachable remote switches active server.
	req, _ = http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/server", bytes.NewBufferString(`{"server_url":"https://sumi.team"}`))
	req.Header.Set("Origin", defaultServerURL)
	req.Header.Set("Authorization", "Bearer "+cs.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST switch: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on switch, got %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode switched state: %v", err)
	}
	if state.IsLocal || state.ServerURL != "https://sumi.team" {
		t.Fatalf("unexpected switched state: %+v", state)
	}

	// Open-URL rejects wrong origin, invalid schemes, and forwards safe http/https/mailto URLs.
	req, _ = http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/open-url", bytes.NewBufferString(`{"url":"https://example.com"}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer "+cs.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open-url wrong origin: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for foreign origin on open-url, got %d", resp.StatusCode)
	}

	for _, bad := range []string{
		"",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"data:text/html,hi",
		"https:///no-host",
		"mailto:",
	} {
		payload, _ := json.Marshal(map[string]string{"url": bad})
		req, _ = http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/open-url", bytes.NewReader(payload))
		req.Header.Set("Origin", "https://sumi.team")
		req.Header.Set("Authorization", "Bearer "+cs.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("open-url bad %q: %v", bad, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for %q, got %d", bad, resp.StatusCode)
		}
	}

	for _, good := range []string{
		"https://example.com/docs?q=1#top",
		"http://127.0.0.1:3000/preview",
		"mailto:team@example.com",
	} {
		payload, _ := json.Marshal(map[string]string{"url": good})
		req, _ = http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/open-url", bytes.NewReader(payload))
		req.Header.Set("Origin", "https://sumi.team")
		req.Header.Set("Authorization", "Bearer "+cs.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("open-url good %q: %v", good, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for %q, got %d", good, resp.StatusCode)
		}
	}
	if len(opened) != 3 || opened[0] != "https://example.com/docs?q=1#top" || opened[1] != "http://127.0.0.1:3000/preview" || opened[2] != "mailto:team@example.com" {
		t.Fatalf("unexpected opened URLs: %+v", opened)
	}
}

func TestControlServerHideWindow(t *testing.T) {
	rt := newRuntime(Config{
		ServerURL:     defaultServerURL,
		ComputerToken: "secret-computer-token",
		DataDir:       t.TempDir(),
	}, slog.New(slog.DiscardHandler))
	rt.healthCheck = func(_ context.Context, u string) bool { return u == defaultServerURL }
	rt.stopComputer = func(context.Context, string) error { return nil }
	defer rt.detach()

	var cs *controlServer
	onSwitch := func(ctx context.Context, rawURL string) (desktopServerState, error) {
		return cs.currentState(), nil
	}

	var hideCalls int
	onHideWindow := func() error {
		hideCalls++
		return nil
	}

	var err error
	cs, err = startControlServer(rt, onSwitch, func(string) error { return nil }, onHideWindow, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("start control server: %v", err)
	}
	defer cs.shutdown()

	req, _ := http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/hide-window", nil)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer "+cs.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("hide-window foreign origin: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for a foreign origin, got %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, cs.baseURL+"/desktop/hide-window", nil)
	req.Header.Set("Origin", defaultServerURL)
	req.Header.Set("Authorization", "Bearer "+cs.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("hide-window GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for GET, got %d", resp.StatusCode)
	}

	if hideCalls != 0 {
		t.Fatalf("hide-window ran for rejected requests: %d", hideCalls)
	}

	req, _ = http.NewRequest(http.MethodPost, cs.baseURL+"/desktop/hide-window", nil)
	req.Header.Set("Origin", defaultServerURL)
	req.Header.Set("Authorization", "Bearer "+cs.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("hide-window POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if hideCalls != 1 {
		t.Fatalf("expected exactly one hide call, got %d", hideCalls)
	}
}
