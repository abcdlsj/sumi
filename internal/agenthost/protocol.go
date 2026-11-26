// Package agenthost runs a thin per-agent sidecar process (`sumi agent-host`) that owns the
// agent's SessionDriver (ACP subprocess stdio pipes or Builtin harness) over a local Unix socket.
// Restarting `sumi computer` detaches from the socket without cancelling in-flight turns; the new
// daemon re-attaches and resumes streaming turn activity and results.
package agenthost

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/abcdlsj/sumi/internal/datadir"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

// ProtocolVersion bumps only on breaking changes to the local host.sock HTTP/NDJSON contract.
const ProtocolVersion = 1

// ErrDetaching is used as a context cancel cause when `sumi computer` detaches for a hot upgrade.
// Unlike normal cancellation, ErrDetaching closes the local HTTP stream without cancelling the
// turn inside agent-host.
var ErrDetaching = errors.New("computer daemon detaching")

// SocketPath returns the deterministic Unix socket path for an agent's sidecar.
func SocketPath(agentHome string) string {
	return datadir.DeterministicSocketPath(filepath.Join(agentHome, "host.sock"), "sumi-ah", agentHome)
}

// MetaPath returns the metadata file path written by a live sidecar.
func MetaPath(agentHome string) string {
	return filepath.Join(agentHome, "host.json")
}

// SpecHash computes a stable content digest of an AssignedAgentSpec (including credentials)
// so a sidecar is only re-attached when its exact runtime configuration matches.
func SpecHash(spec protocol.AssignedAgentSpec) string {
	raw, _ := json.Marshal(spec)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// BuildHash fields (Meta, StatusResponse) carry the lowercase hex SHA-256 of the sidecar's own
// executable. Semver alone cannot tell local rebuilds apart, so AttachOrSpawn compares this
// against the binary it would spawn and retires idle sidecars still running an older build.
// An empty value means the sidecar predates build reporting and is treated as a mismatch.

// Meta is persisted at <agentHome>/host.json (0600) without secrets or prompts.
type Meta struct {
	ProtocolVersion int       `json:"protocol_version"`
	PID             int       `json:"pid"`
	HostVersion     string    `json:"host_version"`
	SocketPath      string    `json:"socket_path"`
	SpecHash        string    `json:"spec_hash"`
	BuildHash       string    `json:"build_hash,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

type StatusResponse struct {
	ProtocolVersion int              `json:"protocol_version"`
	PID             int              `json:"pid"`
	HostVersion     string           `json:"host_version"`
	SpecHash        string           `json:"spec_hash"`
	BuildHash       string           `json:"build_hash,omitempty"`
	Configured      bool             `json:"configured"`
	Busy            bool             `json:"busy"`
	TurnID          string           `json:"turn_id,omitempty"`
	FocusConvID     string           `json:"focus_conv_id,omitempty"`
	LastActivity    *driver.Activity `json:"last_activity,omitempty"`
	LastCompleted   *CompletedTurn   `json:"last_completed,omitempty"`
	StartedAt       time.Time        `json:"started_at"`
}

type CompletedTurn struct {
	TurnID      string            `json:"turn_id"`
	FocusConvID string            `json:"focus_conv_id,omitempty"`
	Result      driver.TurnResult `json:"result"`
	ErrorCode   string            `json:"error_code,omitempty"`
	ErrorDetail string            `json:"error_detail,omitempty"`
	FinishedAt  time.Time         `json:"finished_at"`
}

type ConfigureRequest struct {
	Spec      protocol.AssignedAgentSpec `json:"spec"`
	AgentHome string                     `json:"agent_home"`
}

type StartTurnRequest struct {
	TurnID         string   `json:"turn_id"`
	FocusConvID    string   `json:"focus_conv_id,omitempty"`
	SystemPrompt   string   `json:"system_prompt,omitempty"`
	StartupContext string   `json:"startup_context,omitempty"`
	UserPrompt     string   `json:"user_prompt"`
	WorkDir        string   `json:"work_dir"`
	MemoryDir      string   `json:"memory_dir,omitempty"`
	Env            []string `json:"env,omitempty"`
}

// Event is one NDJSON line streamed from /turns and /turns/{id}/wait.
type Event struct {
	Type        string             `json:"type"` // "activity" | "done"
	Activity    *driver.Activity   `json:"activity,omitempty"`
	Result      *driver.TurnResult `json:"result,omitempty"`
	ErrorCode   string             `json:"error_code,omitempty"`
	ErrorDetail string             `json:"error_detail,omitempty"`
}
