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
	"time"

	"github.com/abcdlsj/sumi/internal/datadir"
)

var defaultProxyRetryDelays = []time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
	1500 * time.Millisecond,
	2000 * time.Millisecond,
}

func (d *Daemon) startCapabilitySocket() error {
	preferred := filepath.Join(d.cfg.DataDir, "capability.sock")
	sockPath := datadir.DeterministicSocketPath(preferred, "sumi-cap", d.cfg.DataDir)
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

func isRetryableProxyRequest(method, idempotencyKey string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete:
		return true
	default:
		return strings.TrimSpace(idempotencyKey) != ""
	}
}

func isTransientProxyStatus(code int) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

// handleCapabilityProxy verifies per-agent SUMI_DRIVER_TOKEN and forwards requests to Server
// using the Computer bearer token so agents never hold Server credentials.
// Safe/idempotent requests retry briefly across Server restarts so in-flight turns do not fail.
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
	retryable := isRetryableProxyRequest(r.Method, r.Header.Get("Idempotency-Key"))
	delays := d.proxyRetryDelays
	if delays == nil {
		delays = defaultProxyRetryDelays
	}

	var resp *http.Response
	var err error
	attempts := 1
	if retryable {
		attempts += len(delays)
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(delays[attempt-1])
			select {
			case <-r.Context().Done():
				timer.Stop()
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"server unreachable"}}`))
				return
			case <-timer.C:
			}
		}
		req, reqErr := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
		if reqErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		req.Header.Set("Authorization", "Bearer "+d.cfg.ComputerToken)
		for _, h := range []string{"Content-Type", "Idempotency-Key"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
		resp, err = d.httpClient.Do(req)
		if err != nil {
			continue
		}
		if retryable && isTransientProxyStatus(resp.StatusCode) && attempt+1 < attempts {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			resp = nil
			continue
		}
		break
	}
	if err != nil || resp == nil {
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
