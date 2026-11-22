package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/abcdlsj/sumi/internal/computer"
)

// ServerStatus is served on ~/.sumi/run/server.ctl.sock GET /status.
type ServerStatus struct {
	PID        int       `json:"pid"`
	Version    string    `json:"version"`
	BinaryHash string    `json:"binary_hash"`
	Addr       string    `json:"addr"`
	DBPath     string    `json:"db_path"`
	DataRoot   string    `json:"data_root"`
	StartedAt  time.Time `json:"started_at"`
}

// HandoffRequest triggers an in-place verified server replacement with TCP FD inheritance.
type HandoffRequest struct {
	TargetBinary string `json:"target_binary"`
}

// HandoffResponse reports a completed verified server replacement.
type HandoffResponse struct {
	OldPID     int    `json:"old_pid"`
	NewPID     int    `json:"new_pid"`
	TargetHash string `json:"target_hash"`
	BackupPath string `json:"backup_path,omitempty"`
}

// ComputerStatus is served on ~/.sumi/run/computer.ctl.sock GET /status.
type ComputerStatus struct {
	PID        int                   `json:"pid"`
	Version    string                `json:"version"`
	BinaryHash string                `json:"binary_hash"`
	ServerURL  string                `json:"server_url"`
	DataDir    string                `json:"data_dir"`
	DataRoot   string                `json:"data_root"`
	Daemon     computer.DaemonStatus `json:"daemon"`
	StartedAt  time.Time             `json:"started_at"`
}

// ComputerUpgradeRequest triggers a hot (sidecar-preserving) or drained Computer daemon upgrade.
type ComputerUpgradeRequest struct {
	TargetBinary    string `json:"target_binary"`
	Mode            string `json:"mode,omitempty"` // "hot" (default) | "drain"
	DeadlineSeconds int    `json:"deadline_seconds,omitempty"`
}

// ComputerUpgradeResponse reports a completed Computer daemon upgrade.
type ComputerUpgradeResponse struct {
	OldPID     int                    `json:"old_pid"`
	NewPID     int                    `json:"new_pid"`
	TargetHash string                 `json:"target_hash"`
	Mode       string                 `json:"mode"`
	Drain      *computer.DrainOutcome `json:"drain,omitempty"`
}

// ControlServer exposes a 0600 Unix-socket HTTP endpoint whose listener can be released
// independently of active HTTP connections during verified handoff.
type ControlServer struct {
	sockPath string
	handler  http.Handler

	mu      sync.Mutex
	ln      net.Listener
	httpSrv *http.Server
}

func NewControlServer(sockPath string, handler http.Handler) *ControlServer {
	return &ControlServer{
		sockPath: sockPath,
		handler:  handler,
	}
}

// Listen binds sockPath (0600) and starts serving asynchronously.
func (c *ControlServer) Listen() error {
	if err := os.MkdirAll(filepath.Dir(c.sockPath), 0o700); err != nil {
		return fmt.Errorf("create control dir: %w", err)
	}
	_ = os.Remove(c.sockPath)
	ln, err := net.Listen("unix", c.sockPath)
	if err != nil {
		return fmt.Errorf("listen control socket: %w", err)
	}
	_ = os.Chmod(c.sockPath, 0o600)

	srv := &http.Server{Handler: c.handler}
	c.mu.Lock()
	c.ln = ln
	c.httpSrv = srv
	c.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

// ReleaseListener closes the listening socket and unlinks sockPath while keeping any already
// accepted HTTP request connections open so the caller receives the handoff response cleanly.
func (c *ControlServer) ReleaseListener() {
	c.mu.Lock()
	ln := c.ln
	c.ln = nil
	c.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
		_ = os.Remove(c.sockPath)
	}
}

// Close shuts down the control server and removes sockPath if owned by this listener.
func (c *ControlServer) Close() {
	c.mu.Lock()
	ln, srv := c.ln, c.httpSrv
	c.ln, c.httpSrv = nil, nil
	c.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
		_ = os.Remove(c.sockPath)
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
}

// ControlClient talks to a local control socket (server.ctl.sock or computer.ctl.sock).
type ControlClient struct {
	sockPath   string
	httpClient *http.Client
}

func NewControlClient(sockPath string) *ControlClient {
	tr := &http.Transport{
		// Disable keep-alives so calls right after handoff/upgrade always dial the new process.
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}
	return &ControlClient{
		sockPath:   sockPath,
		httpClient: &http.Client{Transport: tr},
	}
}

func (c *ControlClient) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readControlError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *ControlClient) PostJSON(ctx context.Context, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readControlError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func readControlError(resp *http.Response) error {
	var payload struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &payload) == nil && payload.Error != "" {
		return errors.New(payload.Error)
	}
	return fmt.Errorf("control request failed with status %d", resp.StatusCode)
}

func writeControlJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeControlError(w http.ResponseWriter, status int, msg string) {
	writeControlJSON(w, status, map[string]string{"error": msg})
}

func WritePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadPID(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

func RemovePIDIfSelf(path string) {
	if pid, err := ReadPID(path); err == nil && pid == os.Getpid() {
		_ = os.Remove(path)
	}
}

func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func killCandidate(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && ProcessAlive(pid) {
		time.Sleep(30 * time.Millisecond)
	}
	if ProcessAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
