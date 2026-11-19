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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

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
	router *chi.Mux
	logger *slog.Logger

	pairingLimiter *ipLimiter
}

func New(svc *app.Service, cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	hub := newComputerHub(logger)
	svc.SetNotifier(hub)

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	if cfg.WebOrigin != "" {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   []string{cfg.WebOrigin},
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Content-Type", "Idempotency-Key", "Authorization"},
			AllowCredentials: true,
		}))
	}

	s := &Server{
		pairingLimiter: newIPLimiter(pairingRequestsPerMinute, time.Minute),
		svc:            svc,
		cfg:            cfg,
		hub:            hub,
		router:         r,
		logger:         logger,
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) Shutdown(ctx context.Context) {
	s.hub.ShutdownAll(ctx, "server shutting down")
}

func (s *Server) registerRoutes() {
	r := s.router
	r.Get("/api/v1/health", s.handleHealth)

	// Auth
	r.Post("/api/v1/auth/register", s.handleRegister)
	r.Post("/api/v1/auth/login", s.handleLogin)
	r.Post("/api/v1/auth/logout", s.handleLogout)
	r.Get("/api/v1/auth/me", s.handleMe)

	// Spaces
	r.Get("/api/v1/spaces", s.handleListSpaces)
	r.Post("/api/v1/spaces", s.handleCreateSpace)

	// Confirm-based Computer pairing
	r.Get("/api/v1/pairings/{pairingID}", s.handleViewPairing)

	// Space-scoped Human APIs
	r.Route("/api/v1/spaces/{spaceSlug}", func(r chi.Router) {
		r.Get("/bootstrap", s.handleBootstrap)
		r.Get("/events", s.handleSSE)

		r.Get("/members", s.handleListMembers)
		r.Put("/members/{memberID}/permissions/{perm}", s.handleSetPermission)

		r.Get("/computers", s.handleListComputers)
		r.Post("/computers/pair", s.handlePairComputer)
		r.Post("/computers/pairings/{pairingID}/confirm", s.handleConfirmPairing)

		r.Post("/agents", s.handleCreateAgent)
		r.Post("/agents/{agentID}/control", s.handleControlAgent)
		r.Put("/agents/{agentID}/driver", s.handleUpdateAgentDriver)

		r.Get("/conversations", s.handleListConversations)
		r.Post("/attachments", s.handleUploadAttachment)
		r.Get("/attachments/{attachmentID}", s.handleDownloadAttachment)
		r.Post("/channels", s.handleCreateChannel)
		r.Post("/dm", s.handleCreateDM)
		r.Get("/conversations/{convID}/messages", s.handleListMessages)
		r.Post("/conversations/{convID}/messages", s.handleSendMessage)
		r.Post("/conversations/{convID}/read", s.handleMarkRead)
		r.Post("/conversations/{convID}/done", s.handleMarkDone)
		r.Put("/conversations/{convID}/notify", s.handleSetNotifyLevel)
		r.Put("/conversations/{convID}/follow", s.handleSetFollowing)

		r.Get("/inbox", s.handleListInbox)

		r.Get("/tasks", s.handleListTasks)
		r.Post("/tasks", s.handleCreateTask)
		r.Get("/tasks/{taskID}", s.handleGetTask)
		r.Post("/tasks/{taskID}/conversation", s.handleEnsureTaskConv)
		r.Post("/tasks/{taskID}/claim", s.handleClaimTask)
		r.Post("/tasks/{taskID}/unclaim", s.handleUnclaimTask)
		r.Post("/tasks/{taskID}/assign", s.handleAssignTask)
		r.Put("/tasks/{taskID}/status", s.handleUpdateTaskStatus)
		r.Patch("/tasks/{taskID}", s.handleAmendTask)
	})

	// Computer WebSocket + Computer-brokered Agent APIs
	r.Get("/ws/computer", s.handleComputerWS)
	r.Post("/api/v1/computer/pairings", s.handleCreatePairing)
	r.Get("/api/v1/computer/pairings/{pairingID}", s.handlePollPairing)
	r.Route("/api/v1/computer/agents/{agentID}", func(r chi.Router) {
		r.Get("/inbox", s.handleAgentInbox)
		r.Post("/attachments", s.handleAgentUploadAttachment)
		r.Get("/attachments/{attachmentID}", s.handleAgentDownloadAttachment)
		r.Get("/conversations", s.handleAgentConversations)
		r.Get("/conversations/{convID}/messages", s.handleAgentReadMessages)
		r.Post("/conversations/{convID}/messages", s.handleAgentSendMessage)
		r.Put("/conversations/{convID}/follow", s.handleAgentSetFollowing)
		r.Put("/conversations/{convID}/notify", s.handleAgentSetNotifyLevel)
		r.Post("/dm", s.handleAgentDM)
		r.Get("/members", s.handleAgentMembers)
		r.Post("/agents", s.handleAgentCreateAgent)
		r.Post("/channels", s.handleAgentCreateChannel)
		r.Get("/tasks", s.handleAgentListTasks)
		r.Post("/tasks", s.handleAgentCreateTask)
		r.Post("/tasks/{taskID}/conversation", s.handleAgentEnsureTaskConv)
		r.Post("/tasks/{taskID}/claim", s.handleAgentClaimTask)
		r.Post("/tasks/{taskID}/unclaim", s.handleAgentUnclaimTask)
		r.Post("/tasks/{taskID}/assign", s.handleAgentAssignTask)
		r.Put("/tasks/{taskID}/status", s.handleAgentUpdateTaskStatus)
		r.Patch("/tasks/{taskID}", s.handleAgentAmendTask)
	})

	if s.cfg.StaticFS != nil {
		r.Handle("/*", s.cfg.StaticFS)
	}
}

func writeSendResult(w http.ResponseWriter, res app.SendMessageResult) {
	switch {
	case res.Held:
		writeJSON(w, http.StatusConflict, res)
	case res.Replayed:
		writeJSON(w, http.StatusOK, res)
	default:
		writeJSON(w, http.StatusCreated, res)
	}
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
		case domain.CodeExpired:
			status = http.StatusGone
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
	return s.svc.ResolveHumanMemberInSpace(r.Context(), u.ID, chi.URLParam(r, "spaceSlug"))
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
	err = s.svc.SetMemberPermission(r.Context(), actor, chi.URLParam(r, "memberID"), domain.Permission(chi.URLParam(r, "perm")), body.Granted)
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
	agent, err := s.svc.CreateAgent(r.Context(), actor, app.CreateAgentInput{
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
	writeJSON(w, http.StatusCreated, map[string]any{"agent": agent})
}

func (s *Server) handleUpdateAgentDriver(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var body struct {
		DriverKind   domain.DriverKind   `json:"driver_kind"`
		DriverConfig domain.DriverConfig `json:"driver_config"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	agent, err := s.svc.UpdateAgentDriver(r.Context(), actor, chi.URLParam(r, "agentID"), body.DriverKind, body.DriverConfig)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": agent})
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
	if err := s.svc.ControlAgent(r.Context(), actor, chi.URLParam(r, "agentID"), body.Action); err != nil {
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
	page, err := s.svc.ListMessages(r.Context(), member, app.ListMessagesInput{
		ConversationID: chi.URLParam(r, "convID"),
		AfterSeq:       afterSeq,
		Limit:          limit,
		MarkRead:       markRead,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	_, actor, err := s.requireSpaceMember(r)
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
	res, err := s.svc.SendMessage(r.Context(), actor, app.SendMessageInput{
		ConversationID:   chi.URLParam(r, "convID"),
		ReplyToMessageID: body.ReplyToMessageID,
		BodyMarkdown:     body.BodyMarkdown,
		AttachmentIDs:    body.AttachmentIDs,
		IdempotencyKey:   r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeSendResult(w, res)
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
	if err := s.svc.MarkRead(r.Context(), actor, chi.URLParam(r, "convID"), body.Seq); err != nil {
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
	if err := s.svc.MarkDone(r.Context(), actor, chi.URLParam(r, "convID")); err != nil {
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
	if err := s.svc.SetNotifyLevel(r.Context(), actor, chi.URLParam(r, "convID"), body.Level); err != nil {
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
	if err := s.svc.SetFollowing(r.Context(), actor, chi.URLParam(r, "convID"), body.Following); err != nil {
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
	task, err := s.svc.GetTask(r.Context(), member, chi.URLParam(r, "taskID"))
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
	task, err := s.svc.EnsureTaskConversation(r.Context(), actor, chi.URLParam(r, "taskID"))
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
	res, err := s.svc.ClaimTask(r.Context(), actor, chi.URLParam(r, "taskID"), false)
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
	task, err := s.svc.UnclaimTask(r.Context(), actor, chi.URLParam(r, "taskID"))
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
		TaskID:           chi.URLParam(r, "taskID"),
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
	task, err := s.svc.UpdateTaskStatus(r.Context(), actor, chi.URLParam(r, "taskID"), body.Status)
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
