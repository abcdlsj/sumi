package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
)

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

func (s *Server) handleUpdateSpace(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	var body struct {
		Name   string `json:"name"`
		Accent string `json:"accent"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	sp, err := s.svc.UpdateSpace(r.Context(), actor, body.Name, body.Accent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": sp})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	members, err := s.svc.ListMembers(r.Context(), actor.SpaceID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleSetPermission(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	var body struct {
		Granted bool `json:"granted"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	err := s.svc.SetMemberPermission(r.Context(), actor, chi.URLParam(r, "memberID"), domain.Permission(chi.URLParam(r, "perm")), body.Granted)
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
	user, err := s.requireUser(r)
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
	comp, token, err := s.svc.PairComputerDirect(r.Context(), user.ID, body.Name, body.Hostname, body.OS)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"computer": comp, "token": token})
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleUpdateAgentDriver(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleControlAgent(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleListAgentWorkspace(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	relPath := r.URL.Query().Get("path")
	includeHidden := r.URL.Query().Get("include_hidden") == "true"
	listing, err := s.svc.ListAgentWorkspace(r.Context(), actor, chi.URLParam(r, "agentID"), relPath, includeHidden)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

func (s *Server) handleReadAgentWorkspaceFile(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	filePath := r.URL.Query().Get("path")
	file, err := s.svc.ReadAgentWorkspaceFile(r.Context(), actor, chi.URLParam(r, "agentID"), filePath)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}
