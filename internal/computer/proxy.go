package computer

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (d *Daemon) startCapabilitySocket() error {
	sockPath := filepath.Join(d.cfg.DataDir, "capability.sock")
	// macOS sockaddr_un.sun_path is limited to 104 bytes; fall back to /tmp when DataDir is deep.
	if len(sockPath) > 96 {
		sockPath = filepath.Join(os.TempDir(), "sumi-"+d.daemonSessionID[:8]+".sock")
	}
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen capability socket: %w", err)
	}
	_ = os.Chmod(sockPath, 0o600)

	srv := &http.Server{Handler: http.HandlerFunc(d.handleCapabilityProxy)}
	d.mu.Lock()
	d.socketPath = sockPath
	d.socketLn = ln
	d.socketServer = srv
	d.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

// handleCapabilityProxy verifies per-agent SUMI_DRIVER_TOKEN and forwards requests to Server
// using the Computer bearer token so agents never hold Server credentials.
func (d *Daemon) handleCapabilityProxy(w http.ResponseWriter, r *http.Request) {
	driverToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	d.mu.RLock()
	agentID := d.tokenToAgent[driverToken]
	d.mu.RUnlock()
	if agentID == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"invalid driver token"}}`))
		return
	}

	targetURL := strings.TrimRight(d.cfg.ServerURL, "/") + "/api/v1/computer/agents/" + agentID + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.ComputerToken)
	for _, h := range []string{"Content-Type", "Idempotency-Key"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := d.httpClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"server unreachable"}}`))
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
