package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

const sessionCookieName = "sumi_session"

type Config struct {
	WebOrigin     string
	SecureCookies bool
	StaticFS      http.Handler // Optional embedded/static web UI handler.
}

type Server struct {
	svc    *app.Service
	cfg    Config
	hub    *computerHub
	mux    *http.ServeMux
	logger *slog.Logger
}

func New(svc *app.Service, cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	hub := newComputerHub(logger)
	svc.SetNotifier(hub)

	s := &Server{
		svc:    svc,
		cfg:    cfg,
		hub:    hub,
		mux:    http.NewServeMux(),
		logger: logger,
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.withCORS(s.mux)
}

func (s *Server) Shutdown(ctx context.Context) {
	s.hub.ShutdownAll(ctx, "server shutting down")
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// Auth
	s.mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)

	// Spaces
	s.mux.HandleFunc("GET /api/v1/spaces", s.handleListSpaces)
	s.mux.HandleFunc("POST /api/v1/spaces", s.handleCreateSpace)

	// Space-scoped Human APIs
	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/events", s.handleSSE)

	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/members", s.handleListMembers)
	s.mux.HandleFunc("PUT /api/v1/spaces/{spaceSlug}/members/{memberID}/permissions/{perm}", s.handleSetPermission)

	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/computers", s.handleListComputers)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/computers/pair", s.handlePairComputer)

	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/agents", s.handleCreateAgent)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/agents/{agentID}/control", s.handleControlAgent)

	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/conversations", s.handleListConversations)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/channels", s.handleCreateChannel)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/dm", s.handleCreateDM)
	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/conversations/{convID}/messages", s.handleListMessages)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/conversations/{convID}/messages", s.handleSendMessage)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/conversations/{convID}/read", s.handleMarkRead)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/conversations/{convID}/done", s.handleMarkDone)
	s.mux.HandleFunc("PUT /api/v1/spaces/{spaceSlug}/conversations/{convID}/notify", s.handleSetNotifyLevel)
	s.mux.HandleFunc("PUT /api/v1/spaces/{spaceSlug}/conversations/{convID}/follow", s.handleSetFollowing)

	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/inbox", s.handleListInbox)

	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/tasks", s.handleListTasks)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/tasks", s.handleCreateTask)
	s.mux.HandleFunc("GET /api/v1/spaces/{spaceSlug}/tasks/{taskID}", s.handleGetTask)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/tasks/{taskID}/conversation", s.handleEnsureTaskConv)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/tasks/{taskID}/claim", s.handleClaimTask)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/tasks/{taskID}/unclaim", s.handleUnclaimTask)
	s.mux.HandleFunc("POST /api/v1/spaces/{spaceSlug}/tasks/{taskID}/assign", s.handleAssignTask)
	s.mux.HandleFunc("PUT /api/v1/spaces/{spaceSlug}/tasks/{taskID}/status", s.handleUpdateTaskStatus)
	s.mux.HandleFunc("PATCH /api/v1/spaces/{spaceSlug}/tasks/{taskID}", s.handleAmendTask)

	// Computer WebSocket + Computer-brokered Agent APIs
	s.mux.HandleFunc("GET /ws/computer", s.handleComputerWS)
	s.mux.HandleFunc("GET /api/v1/computer/agents/{agentID}/inbox", s.handleAgentInbox)
	s.mux.HandleFunc("GET /api/v1/computer/agents/{agentID}/conversations", s.handleAgentConversations)
	s.mux.HandleFunc("GET /api/v1/computer/agents/{agentID}/conversations/{convID}/messages", s.handleAgentReadMessages)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/conversations/{convID}/messages", s.handleAgentSendMessage)
	s.mux.HandleFunc("PUT /api/v1/computer/agents/{agentID}/conversations/{convID}/follow", s.handleAgentSetFollowing)
	s.mux.HandleFunc("PUT /api/v1/computer/agents/{agentID}/conversations/{convID}/notify", s.handleAgentSetNotifyLevel)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/dm", s.handleAgentDM)
	s.mux.HandleFunc("GET /api/v1/computer/agents/{agentID}/members", s.handleAgentMembers)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/agents", s.handleAgentCreateAgent)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/channels", s.handleAgentCreateChannel)
	s.mux.HandleFunc("GET /api/v1/computer/agents/{agentID}/tasks", s.handleAgentListTasks)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/tasks", s.handleAgentCreateTask)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/tasks/{taskID}/claim", s.handleAgentClaimTask)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/tasks/{taskID}/unclaim", s.handleAgentUnclaimTask)
	s.mux.HandleFunc("POST /api/v1/computer/agents/{agentID}/tasks/{taskID}/assign", s.handleAgentAssignTask)
	s.mux.HandleFunc("PUT /api/v1/computer/agents/{agentID}/tasks/{taskID}/status", s.handleAgentUpdateTaskStatus)
	s.mux.HandleFunc("PATCH /api/v1/computer/agents/{agentID}/tasks/{taskID}", s.handleAgentAmendTask)

	if s.cfg.StaticFS != nil {
		s.mux.Handle("/", s.cfg.StaticFS)
	}
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.cfg.WebOrigin != "" && origin == s.cfg.WebOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	var domErr *domain.Error
	if errors.As(err, &domErr) {
		status := http.StatusInternalServerError
		switch domErr.Code {
		case domain.CodeInvalidArgument:
			status = http.StatusBadRequest
		case domain.CodeUnauthenticated:
			status = http.StatusUnauthorized
		case domain.CodePermissionDenied:
			status = http.StatusForbidden
		case domain.CodeNotFound:
			status = http.StatusNotFound
		case domain.CodeConflict, domain.CodeHeld:
			status = http.StatusConflict
		case domain.CodeRateLimited:
			status = http.StatusTooManyRequests
		case domain.CodeUnavailable:
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, map[string]any{"error": domErr})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": domain.NewError(domain.CodeInternal, "internal server error"),
	})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(dst); err != nil {
		return domain.NewError(domain.CodeInvalidArgument, "invalid JSON request body")
	}
	return nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.SecureCookies,
		MaxAge:   30 * 24 * 3600,
	})
}

func (s *Server) requireUser(r *http.Request) (domain.User, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return domain.User{}, domain.NewError(domain.CodeUnauthenticated, "not signed in")
	}
	return s.svc.AuthenticateBrowserSession(r.Context(), c.Value)
}

func (s *Server) requireSpaceMember(r *http.Request) (domain.Space, domain.Member, error) {
	u, err := s.requireUser(r)
	if err != nil {
		return domain.Space{}, domain.Member{}, err
	}
	return s.svc.ResolveHumanMemberInSpace(r.Context(), u.ID, r.PathValue("spaceSlug"))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": 2})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.Register(r.Context(), body.Email, body.Password, body.DisplayName)
	if err != nil {
		writeError(w, err)
		return
	}
	s.setSessionCookie(w, res.SessionToken)
	writeJSON(w, http.StatusCreated, map[string]any{"user": res.User})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	s.setSessionCookie(w, res.SessionToken)
	writeJSON(w, http.StatusOK, map[string]any{"user": res.User})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.svc.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r)
	if err != nil {
		writeError(w, err)
		return
	}
	spaces, err := s.svc.ListUserSpaces(r.Context(), u.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "spaces": spaces})
}

func (s *Server) handleListSpaces(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r)
	if err != nil {
		writeError(w, err)
		return
	}
	spaces, err := s.svc.ListUserSpaces(r.Context(), u.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces})
}

func (s *Server) handleCreateSpace(w http.ResponseWriter, r *http.Request) {
	u, err := s.requireUser(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	sp, member, err := s.svc.CreateSpace(r.Context(), u, body.Slug, body.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"space": sp, "member": member})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	sp, member, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	members, err := s.svc.ListMembers(r.Context(), sp.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	convs, err := s.svc.ListConversations(r.Context(), member)
	if err != nil {
		writeError(w, err)
		return
	}
	inbox, err := s.svc.ListInbox(r.Context(), member, "needs_me")
	if err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.svc.ListTasks(r.Context(), member, "active", "")
	if err != nil {
		writeError(w, err)
		return
	}
	computers, err := s.svc.ListComputers(r.Context(), sp.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"space":          sp,
		"current_member": member,
		"members":        members,
		"conversations":  convs,
		"inbox":          inbox,
		"tasks":          tasks,
		"computers":      computers,
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	sp, _, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, domain.NewError(domain.CodeInternal, "streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.svc.Hub().Subscribe(sp.ID)
	defer cancel()

	// Replay missed events if Last-Event-ID is present.
	if lastID := strings.TrimSpace(r.Header.Get("Last-Event-ID")); lastID != "" {
		if seq, err := strconv.ParseInt(lastID, 10, 64); err == nil {
			rows, err := s.svc.Store().ReadDB().QueryContext(r.Context(),
				`SELECT sequence, kind, payload_json, created_at FROM events WHERE space_id = ? AND sequence > ? ORDER BY sequence ASC LIMIT 200`,
				sp.ID, seq,
			)
			if err == nil {
				for rows.Next() {
					var ev domain.SpaceEvent
					var createdAt string
					if err := rows.Scan(&ev.Sequence, &ev.Kind, &ev.PayloadJSON, &createdAt); err == nil {
						ev.SpaceID = sp.ID
						ev.CreatedAt = store.ParseTime(createdAt)
						writeSSEEvent(w, ev)
					}
				}
				_ = rows.Close()
				flusher.Flush()
			}
		}
	}

	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			writeSSEEvent(w, ev)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, ev domain.SpaceEvent) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Sequence, ev.Kind, ev.PayloadJSON)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	sp, _, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	members, err := s.svc.ListMembers(r.Context(), sp.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleSetPermission(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Granted bool `json:"granted"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	err = s.svc.SetMemberPermission(r.Context(), actor, r.PathValue("memberID"), domain.Permission(r.PathValue("perm")), body.Granted)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListComputers(w http.ResponseWriter, r *http.Request) {
	sp, _, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	comps, err := s.svc.ListComputers(r.Context(), sp.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"computers": comps})
}

func (s *Server) handlePairComputer(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		OS       string `json:"os"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	comp, token, err := s.svc.PairComputerDirect(r.Context(), actor, body.Name, body.Hostname, body.OS)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"computer": comp, "token": token})
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		DisplayName          string            `json:"display_name"`
		RoleText             string            `json:"role_text"`
		ComputerID           string            `json:"computer_id"`
		DriverKind           domain.DriverKind `json:"driver_kind"`
		DriverConfigJSON     string            `json:"driver_config_json"`
		SourceConversationID string            `json:"source_conversation_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	agent, err := s.svc.CreateAgent(r.Context(), actor, app.CreateAgentInput{
		DisplayName:          body.DisplayName,
		RoleText:             body.RoleText,
		ComputerID:           body.ComputerID,
		DriverKind:           body.DriverKind,
		DriverConfigJSON:     body.DriverConfigJSON,
		SourceConversationID: body.SourceConversationID,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent})
}

func (s *Server) handleControlAgent(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := s.svc.ControlAgent(r.Context(), actor, r.PathValue("agentID"), body.Action); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	_, member, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	convs, err := s.svc.ListConversations(r.Context(), member)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Slug      string                  `json:"slug"`
		Kind      domain.ConversationKind `json:"kind"`
		Topic     string                  `json:"topic"`
		MemberIDs []string                `json:"member_ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if body.Kind == "" {
		body.Kind = domain.ConversationPublic
	}
	conv, err := s.svc.CreateChannel(r.Context(), actor, app.CreateChannelInput{
		Slug:      body.Slug,
		Kind:      body.Kind,
		Topic:     body.Topic,
		MemberIDs: body.MemberIDs,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": conv})
}

func (s *Server) handleCreateDM(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	conv, err := s.svc.GetOrCreateDM(r.Context(), actor, body.PeerMemberID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	_, member, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	markRead := r.URL.Query().Get("mark_read") == "true"
	msgs, readSeq, err := s.svc.ListMessages(r.Context(), member, app.ListMessagesInput{
		ConversationID: r.PathValue("convID"),
		AfterSeq:       afterSeq,
		Limit:          limit,
		MarkRead:       markRead,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs, "read_seq": readSeq})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		BodyMarkdown     string `json:"body_markdown"`
		ReplyToMessageID string `json:"reply_to_message_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.SendMessage(r.Context(), actor, app.SendMessageInput{
		ConversationID:   r.PathValue("convID"),
		ReplyToMessageID: body.ReplyToMessageID,
		BodyMarkdown:     body.BodyMarkdown,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		Seq int64 `json:"seq"`
	}
	_ = decodeJSON(r, &body)
	if err := s.svc.MarkRead(r.Context(), actor, r.PathValue("convID"), body.Seq); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMarkDone(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.svc.MarkDone(r.Context(), actor, r.PathValue("convID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetNotifyLevel(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	if err := s.svc.SetNotifyLevel(r.Context(), actor, r.PathValue("convID"), body.Level); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetFollowing(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	if err := s.svc.SetFollowing(r.Context(), actor, r.PathValue("convID"), body.Following); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListInbox(w http.ResponseWriter, r *http.Request) {
	_, member, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	items, err := s.svc.ListInbox(r.Context(), member, r.URL.Query().Get("filter"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	_, member, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	tasks, err := s.svc.ListTasks(r.Context(), member, r.URL.Query().Get("status"), r.URL.Query().Get("assignee"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	task, err := s.svc.CreateTask(r.Context(), actor, app.CreateTaskInput{
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

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	_, member, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.GetTask(r.Context(), member, r.PathValue("taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleEnsureTaskConv(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.EnsureTaskConversation(r.Context(), actor, r.PathValue("taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleClaimTask(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	res, err := s.svc.ClaimTask(r.Context(), actor, r.PathValue("taskID"), false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleUnclaimTask(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	task, err := s.svc.UnclaimTask(r.Context(), actor, r.PathValue("taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleAssignTask(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	res, err := s.svc.AssignTask(r.Context(), actor, app.AssignTaskInput{
		TaskID:           r.PathValue("taskID"),
		AssigneeMemberID: body.AssigneeMemberID,
		Reason:           body.Reason,
		ExpectedRevision: body.ExpectedRevision,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleUpdateTaskStatus(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	task, err := s.svc.UpdateTaskStatus(r.Context(), actor, r.PathValue("taskID"), body.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleAmendTask(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	task, err := s.svc.AmendTask(r.Context(), actor, app.AmendTaskInput{
		TaskID:           r.PathValue("taskID"),
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
