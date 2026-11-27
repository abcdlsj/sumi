package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/store"
)

type computerConn struct {
	computerID      string
	daemonSessionID string
	// capabilities advertised by the Computer; gate Server -> Computer features on these.
	capabilities []string
	conn         *websocket.Conn
	sendCh       chan protocol.Envelope
}

type pendingWorkspaceReq struct {
	computerID string
	ch         chan protocol.Envelope
}

type computerHub struct {
	mu        sync.RWMutex
	conns     map[string]*computerConn
	pendingMu sync.Mutex
	pending   map[string]pendingWorkspaceReq
	logger    *slog.Logger
}

func newComputerHub(logger *slog.Logger) *computerHub {
	return &computerHub{
		conns:   make(map[string]*computerConn),
		pending: make(map[string]pendingWorkspaceReq),
		logger:  logger,
	}
}

func (h *computerHub) Register(cc *computerConn) {
	h.mu.Lock()
	if prev, ok := h.conns[cc.computerID]; ok && prev != cc {
		_ = prev.conn.Close(websocket.StatusGoingAway, "replaced by newer connection")
	}
	h.conns[cc.computerID] = cc
	h.mu.Unlock()
}

// Unregister removes cc only if no newer connection replaced it.
func (h *computerHub) Unregister(cc *computerConn) bool {
	h.mu.Lock()
	removed := false
	if cur, ok := h.conns[cc.computerID]; ok && cur == cc {
		delete(h.conns, cc.computerID)
		removed = true
	}
	h.mu.Unlock()
	if removed {
		h.pendingMu.Lock()
		for id, p := range h.pending {
			if p.computerID == cc.computerID {
				close(p.ch)
				delete(h.pending, id)
			}
		}
		h.pendingMu.Unlock()
	}
	return removed
}

func (h *computerHub) resolvePending(requestID string, env protocol.Envelope) {
	if requestID == "" {
		return
	}
	h.pendingMu.Lock()
	p, ok := h.pending[requestID]
	if ok {
		delete(h.pending, requestID)
	}
	h.pendingMu.Unlock()
	if ok {
		select {
		case p.ch <- env:
		default:
		}
	}
}

func (h *computerHub) callWorkspace(ctx context.Context, computerID, reqID, requiredCap string, env protocol.Envelope) (protocol.Envelope, error) {
	h.mu.RLock()
	cc := h.conns[computerID]
	h.mu.RUnlock()
	if cc == nil {
		return protocol.Envelope{}, domain.NewError(domain.CodeUnavailable, "computer is offline")
	}
	if !protocol.Has(cc.capabilities, requiredCap) {
		msg := "computer does not support workspace viewing; please update the daemon"
		if requiredCap == protocol.CapAgentMemory {
			msg = "computer does not support memory viewing; please update the daemon"
		}
		return protocol.Envelope{}, domain.NewError(domain.CodeUnavailable, msg)
	}

	ch := make(chan protocol.Envelope, 1)
	h.pendingMu.Lock()
	h.pending[reqID] = pendingWorkspaceReq{computerID: computerID, ch: ch}
	h.pendingMu.Unlock()
	defer func() {
		h.pendingMu.Lock()
		delete(h.pending, reqID)
		h.pendingMu.Unlock()
	}()

	select {
	case cc.sendCh <- env:
	default:
		return protocol.Envelope{}, domain.NewError(domain.CodeUnavailable, "computer connection is busy")
	}

	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case resp, ok := <-ch:
		if !ok {
			return protocol.Envelope{}, domain.NewError(domain.CodeUnavailable, "computer disconnected")
		}
		return resp, nil
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			return protocol.Envelope{}, domain.NewError(domain.CodeUnavailable, "request canceled")
		}
		return protocol.Envelope{}, domain.NewError(domain.CodeUnavailable, "workspace request timed out")
	}
}

func workspaceResultError(code, msg string) error {
	c := domain.Code(code)
	if c == "" {
		c = domain.CodeInternal
	}
	if msg == "" {
		msg = "workspace request failed"
	}
	return domain.NewError(c, msg)
}

func resolveWorkspaceCallScope(scope domain.WorkspaceScope) (requiredCap string, frameScope domain.WorkspaceScope, err error) {
	norm, err := domain.NormalizeWorkspaceScope(scope)
	if err != nil {
		return "", "", err
	}
	if norm == domain.ScopeMemory {
		return protocol.CapAgentMemory, domain.ScopeMemory, nil
	}
	return protocol.CapAgentWorkspace, "", nil
}

func (h *computerHub) ListWorkspace(ctx context.Context, computerID, agentMemberID string, scope domain.WorkspaceScope, relPath string, includeHidden bool) (*domain.WorkspaceListing, error) {
	requiredCap, frameScope, err := resolveWorkspaceCallScope(scope)
	if err != nil {
		return nil, err
	}
	reqID := uuid.NewString()
	resp, err := h.callWorkspace(ctx, computerID, reqID, requiredCap, protocol.Envelope{
		Type: protocol.TypeWorkspaceList,
		WorkspaceList: &protocol.WorkspaceListFrame{
			RequestID:     reqID,
			AgentMemberID: agentMemberID,
			Scope:         frameScope,
			Path:          relPath,
			IncludeHidden: includeHidden,
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.WorkspaceListResult == nil {
		return nil, domain.NewError(domain.CodeInternal, "empty workspace response")
	}
	if resp.WorkspaceListResult.ErrorCode != "" || resp.WorkspaceListResult.Error != "" {
		return nil, workspaceResultError(resp.WorkspaceListResult.ErrorCode, resp.WorkspaceListResult.Error)
	}
	if resp.WorkspaceListResult.Listing == nil {
		return nil, domain.NewError(domain.CodeInternal, "missing workspace listing")
	}
	return resp.WorkspaceListResult.Listing, nil
}

func (h *computerHub) ReadWorkspaceFile(ctx context.Context, computerID, agentMemberID string, scope domain.WorkspaceScope, relPath string) (*domain.WorkspaceFileContent, error) {
	requiredCap, frameScope, err := resolveWorkspaceCallScope(scope)
	if err != nil {
		return nil, err
	}
	reqID := uuid.NewString()
	resp, err := h.callWorkspace(ctx, computerID, reqID, requiredCap, protocol.Envelope{
		Type: protocol.TypeWorkspaceRead,
		WorkspaceRead: &protocol.WorkspaceReadFrame{
			RequestID:     reqID,
			AgentMemberID: agentMemberID,
			Scope:         frameScope,
			Path:          relPath,
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.WorkspaceReadResult == nil {
		return nil, domain.NewError(domain.CodeInternal, "empty workspace file response")
	}
	if resp.WorkspaceReadResult.ErrorCode != "" || resp.WorkspaceReadResult.Error != "" {
		return nil, workspaceResultError(resp.WorkspaceReadResult.ErrorCode, resp.WorkspaceReadResult.Error)
	}
	if resp.WorkspaceReadResult.File == nil {
		return nil, domain.NewError(domain.CodeInternal, "missing workspace file content")
	}
	return resp.WorkspaceReadResult.File, nil
}

func (h *computerHub) SendWake(computerID string, frame protocol.WakeFrame) {
	h.send(computerID, protocol.Envelope{Type: protocol.TypeWake, Wake: &frame})
}

func (h *computerHub) SendControl(computerID string, frame protocol.AgentControlFrame) {
	h.send(computerID, protocol.Envelope{Type: protocol.TypeAgentControl, AgentControl: &frame})
}

func (h *computerHub) send(computerID string, env protocol.Envelope) {
	h.mu.RLock()
	cc := h.conns[computerID]
	h.mu.RUnlock()
	if cc == nil {
		return
	}
	select {
	case cc.sendCh <- env:
	default:
		h.logger.Warn("Computer send buffer full", "computer_id", computerID, "frame_type", env.Type)
	}
}

func (h *computerHub) ShutdownAll(ctx context.Context, reason string) {
	h.mu.Lock()
	list := make([]*computerConn, 0, len(h.conns))
	for _, cc := range h.conns {
		list = append(list, cc)
	}
	h.mu.Unlock()

	for _, cc := range list {
		raw, _ := json.Marshal(protocol.Envelope{
			Type:     protocol.TypeShutdown,
			Shutdown: &protocol.ShutdownFrame{Reason: reason},
		})
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = cc.conn.Write(writeCtx, websocket.MessageText, raw)
		_ = cc.conn.Close(websocket.StatusGoingAway, reason)
		cancel()
	}
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
}

func (s *Server) handleComputerWS(w http.ResponseWriter, r *http.Request) {
	comp, err := s.svc.AuthenticateComputer(r.Context(), bearerToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer wsConn.CloseNow()
	wsConn.SetReadLimit(10 << 20)

	// Expect initial hello frame within 20 seconds.
	helloCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	_, raw, err := wsConn.Read(helloCtx)
	cancel()
	if err != nil {
		return
	}
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Type != protocol.TypeHello || env.Hello == nil {
		_ = wsConn.Close(websocket.StatusPolicyViolation, "expected hello frame")
		return
	}
	if !protocol.Compatible(env.Hello.ProtocolVersion) {
		_ = wsConn.Close(websocket.StatusPolicyViolation, "unsupported protocol version")
		return
	}

	// Register in the hub before marking online in SQLite so concurrent CreateAgent/Wake
	// calls that observe "online" never drop control or wake frames into a missing connection.
	cc := &computerConn{
		computerID:      comp.ID,
		daemonSessionID: env.Hello.DaemonSessionID,
		capabilities:    env.Hello.Capabilities,
		conn:            wsConn,
		sendCh:          make(chan protocol.Envelope, 64),
	}
	s.hub.Register(cc)

	specs, err := s.svc.SetComputerStatus(r.Context(), comp.ID, "online", &app.ComputerHello{
		DaemonVersion: env.Hello.DaemonVersion,
		ACPProviders:  env.Hello.ACPProviders,
	})
	if err != nil {
		s.hub.Unregister(cc)
		_ = wsConn.Close(websocket.StatusInternalError, "failed to mark computer online")
		return
	}
	s.logger.Info("Computer connected", "computer_id", comp.ID, "daemon_session_id", cc.daemonSessionID,
		"protocol_version", env.Hello.ProtocolVersion, "capabilities", strings.Join(cc.capabilities, ","))

	ackRaw, _ := json.Marshal(protocol.Envelope{
		Type: protocol.TypeHelloAck,
		HelloAck: &protocol.HelloAckFrame{
			ProtocolVersion: protocol.Version,
			ComputerID:      comp.ID,
			Agents:          specs,
			Capabilities:    protocol.Supported,
		},
	})
	if err := wsConn.Write(r.Context(), websocket.MessageText, ackRaw); err != nil {
		if s.hub.Unregister(cc) {
			_, _ = s.svc.SetComputerStatus(context.Background(), comp.ID, "offline", nil)
		}
		return
	}

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for frame := range cc.sendCh {
			b, err := json.Marshal(frame)
			if err != nil {
				continue
			}
			wctx, wcancel := context.WithTimeout(r.Context(), 30*time.Second)
			err = wsConn.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return
			}
		}
	}()

	defer func() {
		close(cc.sendCh)
		<-writerDone
		if s.hub.Unregister(cc) {
			_, _ = s.svc.SetComputerStatus(context.Background(), comp.ID, "offline", nil)
			s.logger.Info("Computer disconnected", "computer_id", comp.ID)
		}
	}()

	for {
		readCtx, rcancel := context.WithTimeout(r.Context(), 45*time.Second)
		_, msgBytes, err := wsConn.Read(readCtx)
		rcancel()
		if err != nil {
			return
		}
		var frame protocol.Envelope
		if err := json.Unmarshal(msgBytes, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case protocol.TypeHeartbeat:
			if frame.Heartbeat != nil && frame.Heartbeat.DaemonSessionID == cc.daemonSessionID {
				_, _ = s.svc.SetComputerStatus(r.Context(), comp.ID, "online", nil)
			}
		case protocol.TypeAgentState:
			if frame.AgentState != nil && frame.AgentState.DaemonSessionID == cc.daemonSessionID {
				_ = s.svc.UpdateAgentRuntimeState(
					r.Context(),
					comp.ID,
					frame.AgentState.AgentMemberID,
					frame.AgentState.RuntimeState,
					frame.AgentState.FocusConversationID,
					frame.AgentState.ErrorCode,
					frame.AgentState.ErrorDetail,
					frame.AgentState.Activity,
				)
			}
		case protocol.TypeWorkspaceListResult:
			if frame.WorkspaceListResult != nil {
				s.hub.resolvePending(frame.WorkspaceListResult.RequestID, frame)
			}
		case protocol.TypeWorkspaceReadResult:
			if frame.WorkspaceReadResult != nil {
				s.hub.resolvePending(frame.WorkspaceReadResult.RequestID, frame)
			}
		}
	}
}

// requireComputerAgent verifies the Computer bearer token and that agentID belongs to that Computer.
func (s *Server) requireComputerAgent(r *http.Request) (domain.Computer, domain.Member, error) {
	comp, err := s.svc.AuthenticateComputer(r.Context(), bearerToken(r))
	if err != nil {
		return domain.Computer{}, domain.Member{}, err
	}
	agentID := chi.URLParam(r, "agentID")
	var m domain.Member
	var kind, access, createdAt string
	err = s.svc.Store().ReadDB().QueryRowContext(r.Context(),
		`SELECT m.id, m.space_id, m.kind, m.display_name, m.access_level, m.created_at
		 FROM agents a JOIN members m ON m.id = a.member_id
		 WHERE a.member_id = ? AND a.computer_id = ? AND m.retired_at IS NULL`,
		agentID, comp.ID,
	).Scan(&m.ID, &m.SpaceID, &kind, &m.DisplayName, &access, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Computer{}, domain.Member{}, domain.NewError(domain.CodePermissionDenied, "agent not hosted on this computer")
	}
	if err != nil {
		return domain.Computer{}, domain.Member{}, domain.WrapError(domain.CodeInternal, "query hosted agent", err)
	}
	m.Kind = domain.MemberKind(kind)
	m.AccessLevel = domain.AccessLevel(access)
	m.CreatedAt = store.ParseTime(createdAt)
	return comp, m, nil
}

func (s *Server) handleAgentConversations(w http.ResponseWriter, r *http.Request, agent domain.Member) {
	convs, err := s.svc.ListAgentConversations(r.Context(), agent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleAgentReadMessages(w http.ResponseWriter, r *http.Request, agent domain.Member) {
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	unreadOnly := r.URL.Query().Get("unread") == "true"
	page, err := s.svc.ListMessages(r.Context(), agent, app.ListMessagesInput{
		ConversationID: chi.URLParam(r, "convID"),
		AfterSeq:       afterSeq,
		UnreadOnly:     unreadOnly,
		Limit:          limit,
		MarkRead:       true,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func allowAgentSilentCreate() bool {
	v := strings.TrimSpace(os.Getenv("SUMI_ALLOW_AGENT_SILENT_CREATE"))
	return v == "1" || strings.EqualFold(v, "true")
}

func (s *Server) handleAgentCreateAgent(w http.ResponseWriter, r *http.Request) {
	comp, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		DisplayName          string              `json:"display_name"`
		RoleText             string              `json:"role_text"`
		ComputerID           string              `json:"computer_id"`
		DriverKind           domain.DriverKind   `json:"driver_kind"`
		DriverConfig         domain.DriverConfig `json:"driver_config"`
		SourceConversationID string              `json:"source_conversation_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.ComputerID == "" {
		body.ComputerID = comp.ID
	}
	in := app.CreateAgentInput{
		DisplayName:          body.DisplayName,
		RoleText:             body.RoleText,
		ComputerID:           body.ComputerID,
		DriverKind:           body.DriverKind,
		DriverConfig:         body.DriverConfig,
		SourceConversationID: body.SourceConversationID,
	}
	if allowAgentSilentCreate() {
		created, err := s.svc.CreateAgent(r.Context(), agent, in)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"agent": created})
		return
	}
	msg, err := s.svc.ProposeAgentCreate(r.Context(), agent, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":                 "pending_confirmation",
		"action_card_message_id": msg.ID,
		"conversation_id":        msg.ConversationID,
		"message":                msg,
	})
}
