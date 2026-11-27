package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type desktopServerState struct {
	ServerURL       string `json:"server_url"`
	DefaultLocalURL string `json:"default_local_url"`
	IsLocal         bool   `json:"is_local"`
	ComputerStatus  string `json:"computer_status"`
}

// controlServer exposes a loopback-only endpoint so the Web UI inside the desktop
// window can read and switch the active Server URL or open external links in the
// system browser without touching remote servers.
type controlServer struct {
	baseURL   string
	token     string
	httpSrv   *http.Server
	rt        *runtime
	onSwitch  func(ctx context.Context, rawURL string) (desktopServerState, error)
	onOpenURL func(rawURL string) error
	logger    *slog.Logger
}

func startControlServer(
	rt *runtime,
	onSwitch func(ctx context.Context, rawURL string) (desktopServerState, error),
	onOpenURL func(rawURL string) error,
	logger *slog.Logger,
) (*controlServer, error) {
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return nil, fmt.Errorf("generate control token: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen control server: %w", err)
	}
	cs := &controlServer{
		baseURL:   "http://" + ln.Addr().String(),
		token:     hex.EncodeToString(rawToken),
		rt:        rt,
		onSwitch:  onSwitch,
		onOpenURL: onOpenURL,
		logger:    logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/desktop/server", cs.handleServer)
	mux.HandleFunc("/desktop/open-url", cs.handleOpenURL)
	cs.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := cs.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Desktop control server stopped", "error", err.Error())
		}
	}()
	return cs, nil
}

func (c *controlServer) shutdown() {
	if c == nil || c.httpSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.httpSrv.Shutdown(ctx)
}

// attachBridge appends the loopback control coordinates in the URL fragment so they
// are never transmitted to remote servers in HTTP requests.
func (c *controlServer) attachBridge(rawURL string) string {
	if c == nil {
		return rawURL
	}
	frag := url.Values{}
	frag.Set("desktop_control", c.baseURL)
	frag.Set("desktop_token", c.token)
	return rawURL + "#" + frag.Encode()
}

func (c *controlServer) currentState() desktopServerState {
	cur := c.rt.serverURL()
	return desktopServerState{
		ServerURL:       cur,
		DefaultLocalURL: defaultServerURL,
		IsLocal:         cur == defaultServerURL,
		ComputerStatus:  c.rt.status(),
	}
}

func (c *controlServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	// Only the currently active Sumi origin may call the loopback control bridge.
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	allowedOrigin := c.rt.serverURL()
	if origin != "" {
		if origin != allowedOrigin {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return false
	}

	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if auth == r.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(auth), []byte(c.token)) != 1 {
		writeControlJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "message": "Invalid desktop control token"})
		return false
	}
	return true
}

func validateExternalURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > 4096 {
		return "", errors.New("invalid URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", errors.New("invalid URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Host == "" {
			return "", errors.New("URL host is required")
		}
	case "mailto":
		if strings.TrimSpace(parsed.Opaque) == "" && strings.TrimSpace(parsed.Path) == "" {
			return "", errors.New("mailto address is required")
		}
	default:
		return "", errors.New("unsupported URL scheme")
	}
	return parsed.String(), nil
}

func (c *controlServer) handleServer(w http.ResponseWriter, r *http.Request) {
	if !c.authorize(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeControlJSON(w, http.StatusOK, c.currentState())
	case http.MethodPost:
		var req struct {
			ServerURL string `json:"server_url"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_argument", "message": "Invalid JSON body"})
			return
		}
		state, err := c.onSwitch(r.Context(), req.ServerURL)
		if err != nil {
			status := http.StatusBadRequest
			code := "invalid_argument"
			if errors.Is(err, errServerUnreachable) {
				status = http.StatusBadGateway
				code = "server_unreachable"
			}
			writeControlJSON(w, status, map[string]string{"error": code, "message": err.Error()})
			return
		}
		writeControlJSON(w, http.StatusOK, state)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (c *controlServer) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	if !c.authorize(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
		writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_argument", "message": "Invalid JSON body"})
		return
	}
	target, err := validateExternalURL(req.URL)
	if err != nil {
		writeControlJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_argument", "message": err.Error()})
		return
	}
	if c.onOpenURL == nil {
		writeControlJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable", "message": "Browser launcher unavailable"})
		return
	}
	if err := c.onOpenURL(target); err != nil {
		c.logger.Warn("Cannot open external URL in browser", "error", err.Error())
		writeControlJSON(w, http.StatusInternalServerError, map[string]string{"error": "open_failed", "message": "Unable to open URL in browser"})
		return
	}
	writeControlJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeControlJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
