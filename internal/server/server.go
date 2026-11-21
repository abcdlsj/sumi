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

type actorHandler func(w http.ResponseWriter, r *http.Request, actor domain.Member)

func New(svc *app.Service, cfg Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	hub := newComputerHub(logger)
	svc.SetNotifier(hub)
	svc.SetWorkspaceClient(hub)

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

func (s *Server) asHuman(fn actorHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, actor, err := s.requireSpaceMember(r)
		if err != nil {
			writeError(w, err)
			return
		}
		fn(w, r, actor)
	}
}

func (s *Server) asAgent(fn actorHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, agent, err := s.requireComputerAgent(r)
		if err != nil {
			writeError(w, err)
			return
		}
		fn(w, r, agent)
	}
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

	// Account-owned Computers
	r.Post("/api/v1/computers/pair", s.handlePairComputer)
	r.Post("/api/v1/computers/pairings/{pairingID}/confirm", s.handleConfirmPairing)

	// Space-scoped Human APIs
	r.Route("/api/v1/spaces/{spaceSlug}", func(r chi.Router) {
		r.Get("/bootstrap", s.handleBootstrap)
		r.Patch("/", s.asHuman(s.handleUpdateSpace))
		r.Get("/events", s.handleSSE)

		r.Get("/members", s.asHuman(s.handleListMembers))
		r.Put("/members/{memberID}/permissions/{perm}", s.asHuman(s.handleSetPermission))

		r.Get("/computers", s.handleListComputers)

		r.Post("/agents", s.asHuman(s.handleCreateAgent))
		r.Post("/agents/{agentID}/control", s.asHuman(s.handleControlAgent))
		r.Put("/agents/{agentID}/driver", s.asHuman(s.handleUpdateAgentDriver))
		r.Get("/agents/{agentID}/workspace", s.asHuman(s.handleListAgentWorkspace))
		r.Get("/agents/{agentID}/workspace/file", s.asHuman(s.handleReadAgentWorkspaceFile))

		r.Get("/conversations", s.asHuman(s.handleListConversations))
		r.Get("/channels/archived", s.asHuman(s.handleListArchivedChannels))
		r.Post("/attachments", s.handleUploadAttachment)
		r.Get("/attachments/{attachmentID}", s.handleDownloadAttachment)
		r.Post("/channels", s.asHuman(s.handleCreateChannel))
		r.Post("/channel-groups", s.asHuman(s.handleCreateChannelGroup))
		r.Patch("/channel-groups/{groupID}", s.asHuman(s.handleUpdateChannelGroup))
		r.Delete("/channel-groups/{groupID}", s.asHuman(s.handleDeleteChannelGroup))
		r.Put("/conversations/{convID}/group", s.asHuman(s.handleSetConversationGroup))
		r.Post("/conversations/{convID}/archive", s.asHuman(s.handleArchiveChannel))
		r.Post("/conversations/{convID}/unarchive", s.asHuman(s.handleUnarchiveChannel))
		r.Post("/dm", s.asHuman(s.handleCreateDM))
		r.Get("/conversations/{convID}/messages", s.asHuman(s.handleListMessages))
		r.Post("/conversations/{convID}/messages", s.asHuman(s.handleSendMessage))
		r.Put("/messages/{messageID}/reactions/{emoji}", s.asHuman(s.handleAddReaction))
		r.Delete("/messages/{messageID}/reactions/{emoji}", s.asHuman(s.handleRemoveReaction))
		r.Post("/conversations/{convID}/read", s.asHuman(s.handleMarkRead))
		r.Post("/conversations/{convID}/done", s.asHuman(s.handleMarkDone))
		r.Put("/conversations/{convID}/notify", s.asHuman(s.handleSetNotifyLevel))
		r.Put("/conversations/{convID}/follow", s.asHuman(s.handleSetFollowing))
		r.Get("/conversations/{convID}/members", s.asHuman(s.handleListConversationMembers))
		r.Post("/conversations/{convID}/members", s.asHuman(s.handleInviteToChannel))
		r.Delete("/conversations/{convID}/members/{memberID}", s.asHuman(s.handleRemoveFromChannel))

		r.Get("/inbox", s.asHuman(s.handleListInbox))

		r.Get("/tasks", s.asHuman(s.handleListTasks))
		r.Post("/tasks", s.asHuman(s.handleCreateTask))
		r.Get("/tasks/{taskID}", s.asHuman(s.handleGetTask))
		r.Post("/tasks/{taskID}/conversation", s.asHuman(s.handleEnsureTaskConv))
		r.Post("/tasks/{taskID}/claim", s.asHuman(s.handleClaimTask))
		r.Post("/tasks/{taskID}/unclaim", s.asHuman(s.handleUnclaimTask))
		r.Post("/tasks/{taskID}/assign", s.asHuman(s.handleAssignTask))
		r.Put("/tasks/{taskID}/status", s.asHuman(s.handleUpdateTaskStatus))
		r.Patch("/tasks/{taskID}", s.asHuman(s.handleAmendTask))
	})

	// Computer WebSocket + Computer-brokered Agent APIs
	r.Get("/ws/computer", s.handleComputerWS)
	r.Post("/api/v1/computer/pairings", s.handleCreatePairing)
	r.Get("/api/v1/computer/pairings/{pairingID}", s.handlePollPairing)
	r.Route("/api/v1/computer/agents/{agentID}", func(r chi.Router) {
		r.Get("/inbox", s.asAgent(s.handleListInbox))
		r.Post("/attachments", s.handleAgentUploadAttachment)
		r.Get("/attachments/{attachmentID}", s.handleAgentDownloadAttachment)
		r.Get("/conversations", s.asAgent(s.handleAgentConversations))
		r.Get("/conversations/{convID}/messages", s.asAgent(s.handleAgentReadMessages))
		r.Post("/conversations/{convID}/messages", s.asAgent(s.handleSendMessage))
		r.Put("/messages/{messageID}/reactions/{emoji}", s.asAgent(s.handleAddReaction))
		r.Delete("/messages/{messageID}/reactions/{emoji}", s.asAgent(s.handleRemoveReaction))
		r.Get("/conversations/{convID}/members", s.asAgent(s.handleListConversationMembers))
		r.Post("/conversations/{convID}/members", s.asAgent(s.handleInviteToChannel))
		r.Delete("/conversations/{convID}/members/{memberID}", s.asAgent(s.handleRemoveFromChannel))
		r.Put("/conversations/{convID}/follow", s.asAgent(s.handleSetFollowing))
		r.Put("/conversations/{convID}/notify", s.asAgent(s.handleSetNotifyLevel))
		r.Post("/dm", s.asAgent(s.handleCreateDM))
		r.Get("/members", s.asAgent(s.handleListMembers))
		r.Post("/agents", s.handleAgentCreateAgent)
		r.Post("/channels", s.asAgent(s.handleCreateChannel))
		r.Get("/tasks", s.asAgent(s.handleListTasks))
		r.Post("/tasks", s.asAgent(s.handleCreateTask))
		r.Get("/tasks/{taskID}", s.asAgent(s.handleGetTask))
		r.Post("/tasks/{taskID}/conversation", s.asAgent(s.handleEnsureTaskConv))
		r.Post("/tasks/{taskID}/claim", s.asAgent(s.handleClaimTask))
		r.Post("/tasks/{taskID}/unclaim", s.asAgent(s.handleUnclaimTask))
		r.Post("/tasks/{taskID}/assign", s.asAgent(s.handleAssignTask))
		r.Put("/tasks/{taskID}/status", s.asAgent(s.handleUpdateTaskStatus))
		r.Patch("/tasks/{taskID}", s.asAgent(s.handleAmendTask))
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

func writeTaskWriteResult(w http.ResponseWriter, res app.TaskWriteResult) {
	if res.Held {
		writeJSON(w, http.StatusConflict, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
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
	groups, err := s.svc.ListChannelGroups(r.Context(), member)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"space":          sp,
		"current_member": member,
		"members":        members,
		"conversations":  convs,
		"channel_groups": groups,
		"inbox":          inbox,
		"tasks":          tasks,
		"computers":      computers,
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	sp, viewer, err := s.requireSpaceMember(r)
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
						if s.canReceiveSSEEvent(r.Context(), viewer, ev) {
							writeSSEEvent(w, ev)
						}
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
			if s.canReceiveSSEEvent(r.Context(), viewer, ev) {
				writeSSEEvent(w, ev)
				flusher.Flush()
			}
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// canReceiveSSEEvent prevents private channel, foreign DM, and personal group metadata from leaking over Space SSE.
func (s *Server) canReceiveSSEEvent(ctx context.Context, viewer domain.Member, ev domain.SpaceEvent) bool {
	if ev.PayloadJSON == "" {
		return true
	}
	var meta struct {
		ConversationID string `json:"conversation_id"`
		ChannelID      string `json:"channel_id"`
		MemberID       string `json:"member_id"`
	}
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &meta); err != nil {
		return true
	}
	if ev.Kind == "channel_groups.changed" && meta.MemberID != "" {
		return meta.MemberID == viewer.ID
	}
	if meta.ConversationID != "" {
		return s.svc.CanAccessConversation(ctx, viewer, meta.ConversationID)
	}
	if meta.ChannelID != "" {
		return s.svc.CanAccessConversation(ctx, viewer, meta.ChannelID)
	}
	return true
}

func writeSSEEvent(w http.ResponseWriter, ev domain.SpaceEvent) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Sequence, ev.Kind, ev.PayloadJSON)
}
