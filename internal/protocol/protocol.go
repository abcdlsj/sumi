package protocol

import "github.com/abcdlsj/sumi/internal/domain"

// Version is the protocol revision this build speaks; MinVersion is the oldest peer revision
// it still accepts. Additive features are negotiated through Capabilities instead of bumping
// Version, so mismatched desktop and server builds keep connecting.
const (
	Version    = 2
	MinVersion = 2
)

// Capabilities this build understands. A side only uses a feature when the peer advertised it.
const (
	// CapAgentActivity: the Server accepts agent_state.activity.
	CapAgentActivity = "agent_activity"
)

// Supported lists every capability of this build, advertised in hello and hello_ack.
var Supported = []string{CapAgentActivity}

// Has reports whether caps contains c.
func Has(caps []string, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

// Compatible reports whether a peer speaking version v can connect.
func Compatible(v int) bool { return v >= MinVersion && v <= Version }

// Envelope is the single JSON frame shape exchanged on the Server <-> Computer WebSocket.
type Envelope struct {
	Type string `json:"type"`

	// Computer -> Server frames
	Hello      *HelloFrame      `json:"hello,omitempty"`
	Heartbeat  *HeartbeatFrame  `json:"heartbeat,omitempty"`
	AgentState *AgentStateFrame `json:"agent_state,omitempty"`

	// Server -> Computer frames
	HelloAck     *HelloAckFrame     `json:"hello_ack,omitempty"`
	Wake         *WakeFrame         `json:"wake,omitempty"`
	AgentControl *AgentControlFrame `json:"agent_control,omitempty"`
	Shutdown     *ShutdownFrame     `json:"shutdown,omitempty"`
}

const (
	TypeHello        = "hello"
	TypeHelloAck     = "hello_ack"
	TypeHeartbeat    = "heartbeat"
	TypeWake         = "wake"
	TypeAgentState   = "agent_state"
	TypeAgentControl = "agent_control"
	TypeShutdown     = "shutdown"
)

type HelloFrame struct {
	ProtocolVersion int    `json:"protocol_version"`
	DaemonSessionID string `json:"daemon_session_id"`
	DaemonVersion   string `json:"daemon_version"`
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	// ACPProviders lists ACP providers whose commands resolve on the Computer's PATH.
	ACPProviders []string `json:"acp_providers"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type HelloAckFrame struct {
	ProtocolVersion int                 `json:"protocol_version"`
	ComputerID      string              `json:"computer_id"`
	Agents          []AssignedAgentSpec `json:"agents"`
	Capabilities    []string            `json:"capabilities,omitempty"`
}

type AssignedAgentSpec struct {
	MemberID    string            `json:"member_id"`
	SpaceID     string            `json:"space_id"`
	DisplayName string            `json:"display_name"`
	RoleText    string            `json:"role_text"`
	DriverKind  domain.DriverKind `json:"driver_kind"`
	// DriverConfig is the full config including api_key; it is only sent to the hosting Computer.
	DriverConfig domain.DriverConfig `json:"driver_config"`
}

type HeartbeatFrame struct {
	DaemonSessionID string `json:"daemon_session_id"`
}

type WakeFrame struct {
	AgentMemberID  string `json:"agent_member_id"`
	ConversationID string `json:"conversation_id"`
	Reason         string `json:"reason"`
}

type AgentStateFrame struct {
	DaemonSessionID     string              `json:"daemon_session_id"`
	AgentMemberID       string              `json:"agent_member_id"`
	RuntimeState        domain.RuntimeState `json:"runtime_state"`
	FocusConversationID string              `json:"focus_conversation_id,omitempty"`
	ErrorCode           string              `json:"error_code,omitempty"`
	ErrorDetail         string              `json:"error_detail,omitempty"`
	// Activity accompanies working states with the running tool kind or a stall warning.
	Activity *domain.AgentActivity `json:"activity,omitempty"`
}

type AgentControlFrame struct {
	AgentMemberID string             `json:"agent_member_id"`
	Action        string             `json:"action"` // "provision" | "restart" | "reset_session" | "retire"
	Spec          *AssignedAgentSpec `json:"spec,omitempty"`
}

type ShutdownFrame struct {
	Reason string `json:"reason"`
}
