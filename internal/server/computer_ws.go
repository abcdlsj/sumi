package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

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

type computerHub struct {
	mu     sync.RWMutex
	conns  map[string]*computerConn
	logger *slog.Logger
}

func newComputerHub(logger *slog.Logger) *computerHub {
	return &computerHub{
		conns:  make(map[string]*computerConn),
		logger: logger,
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
	defer h.mu.Unlock()
	if cur, ok := h.conns[cc.computerID]; ok && cur == cc {
		delete(h.conns, cc.computerID)
		return true
	}
	return false
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

	specs, err := s.svc.SetComputerStatus(r.Context(), comp.ID, "online", &app.ComputerHello{
		DaemonVersion: env.Hello.DaemonVersion,
		ACPProviders:  env.Hello.ACPProviders,
	})
	if err != nil {
		_ = wsConn.Close(websocket.StatusInternalError, "failed to mark computer online")
		return
	}

	cc := &computerConn{
		computerID:      comp.ID,
		daemonSessionID: env.Hello.DaemonSessionID,
		capabilities:    env.Hello.Capabilities,
		conn:            wsConn,
		sendCh:          make(chan protocol.Envelope, 64),
	}
	s.hub.Register(cc)
	s.logger.Info("Computer connected", "computer_id", comp.ID, "daemon_session_id", cc.daemonSessionID,
		"protocol_version", env.Hello.ProtocolVersion, "capabilities", strings.Join(cc.capabilities, ","))

	ackRaw, _ := json.Marshal(protocol.Envelope{
		Type: protocol.TypeHelloAck,
		HelloAck: &protocol.HelloAckFrame{
			ProtocolVersion: protocol.Version,
			ComputerID:      comp.ID,
			SpaceID:         comp.SpaceID,
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

func (s *Server) handleAgentInbox(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "needs_me"
	}
	items, err := s.svc.ListInbox(r.Context(), agent, filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAgentConversations(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	convs, err := s.svc.ListConversations(r.Context(), agent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleAgentReadMessages(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
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

func (s *Server) handleAgentSendMessage(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		BodyMarkdown     string   `json:"body_markdown"`
		ReplyToMessageID string   `json:"reply_to_message_id"`
		AttachmentIDs    []string `json:"attachment_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.SendMessage(r.Context(), agent, app.SendMessageInput{
		ConversationID:   chi.URLParam(r, "convID"),
		ReplyToMessageID: body.ReplyToMessageID,
		BodyMarkdown:     body.BodyMarkdown,
		AttachmentIDs:    body.AttachmentIDs,
		EnforceFreshness: true,
		IdempotencyKey:   r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeSendResult(w, res)
}

func (s *Server) handleAgentSetFollowing(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Following bool `json:"following"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := s.svc.SetFollowing(r.Context(), agent, chi.URLParam(r, "convID"), body.Following); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentSetNotifyLevel(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Level domain.NotifyLevel `json:"level"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := s.svc.SetNotifyLevel(r.Context(), agent, chi.URLParam(r, "convID"), body.Level); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAgentDM(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		PeerMemberID string `json:"peer_member_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	conv, err := s.svc.GetOrCreateDM(r.Context(), agent, body.PeerMemberID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleAgentMembers(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	members, err := s.svc.ListMembers(r.Context(), agent.SpaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
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
	created, err := s.svc.CreateAgent(r.Context(), agent, app.CreateAgentInput{
		DisplayName:          body.DisplayName,
		RoleText:             body.RoleText,
		ComputerID:           body.ComputerID,
		DriverKind:           body.DriverKind,
		DriverConfig:         body.DriverConfig,
		SourceConversationID: body.SourceConversationID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": created})
}

func (s *Server) handleAgentCreateChannel(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Slug                 string                  `json:"slug"`
		Kind                 domain.ConversationKind `json:"kind"`
		Topic                string                  `json:"topic"`
		MemberIDs            []string                `json:"member_ids"`
		SourceConversationID string                  `json:"source_conversation_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Kind == "" {
		body.Kind = domain.ConversationPublic
	}
	conv, err := s.svc.CreateChannel(r.Context(), agent, app.CreateChannelInput{
		Slug:                 body.Slug,
		Kind:                 body.Kind,
		Topic:                body.Topic,
		MemberIDs:            body.MemberIDs,
		SourceConversationID: body.SourceConversationID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": conv})
}

func (s *Server) handleAgentListTasks(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.svc.ListTasks(r.Context(), agent, r.URL.Query().Get("status"), r.URL.Query().Get("assignee"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleAgentEnsureTaskConv(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.EnsureTaskConversation(r.Context(), agent, chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleAgentCreateTask(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		ChannelID        string            `json:"channel_id"`
		SourceMessageID  string            `json:"source_message_id"`
		Title            string            `json:"title"`
		Description      string            `json:"description"`
		AssigneeMemberID string            `json:"assignee_member_id"`
		Status           domain.TaskStatus `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.CreateTask(r.Context(), agent, app.CreateTaskInput{
		ChannelID:        body.ChannelID,
		SourceMessageID:  body.SourceMessageID,
		Title:            body.Title,
		Description:      body.Description,
		AssigneeMemberID: body.AssigneeMemberID,
		Status:           body.Status,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"task": task})
}

func (s *Server) handleAgentClaimTask(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.ClaimTask(r.Context(), agent, chi.URLParam(r, "taskID"), true)
	if err != nil {
		writeError(w, err)
		return
	}
	if res.Held {
		writeJSON(w, http.StatusConflict, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAgentUnclaimTask(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.UnclaimTask(r.Context(), agent, chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleAgentAssignTask(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		AssigneeMemberID string `json:"assignee_member_id"`
		Reason           string `json:"reason"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.AssignTask(r.Context(), agent, app.AssignTaskInput{
		TaskID:           chi.URLParam(r, "taskID"),
		AssigneeMemberID: body.AssigneeMemberID,
		Reason:           body.Reason,
		ExpectedRevision: body.ExpectedRevision,
		EnforceFreshness: true,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if res.Held {
		writeJSON(w, http.StatusConflict, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAgentUpdateTaskStatus(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Status domain.TaskStatus `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.UpdateTaskStatus(r.Context(), agent, chi.URLParam(r, "taskID"), body.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleAgentAmendTask(w http.ResponseWriter, r *http.Request) {
	_, agent, err := s.requireComputerAgent(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Title            *string `json:"title"`
		Description      *string `json:"description"`
		ExpectedRevision int64   `json:"expected_revision"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.AmendTask(r.Context(), agent, app.AmendTaskInput{
		TaskID:           chi.URLParam(r, "taskID"),
		Title:            body.Title,
		Description:      body.Description,
		ExpectedRevision: body.ExpectedRevision,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}
