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

	var err error
	cs, err = startControlServer(rt, onSwitch, slog.New(slog.DiscardHandler))
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
}
