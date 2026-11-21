package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
)

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	tasks, err := s.svc.ListTasks(r.Context(), actor, r.URL.Query().Get("status"), r.URL.Query().Get("assignee"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	task, err := s.svc.GetTask(r.Context(), actor, chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleEnsureTaskConv(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	task, err := s.svc.EnsureTaskConversation(r.Context(), actor, chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleClaimTask(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	res, err := s.svc.ClaimTask(r.Context(), actor, chi.URLParam(r, "taskID"), actor.Kind == domain.MemberKindAgent)
	if err != nil {
		writeError(w, err)
		return
	}
	writeTaskWriteResult(w, res)
}

func (s *Server) handleUnclaimTask(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	task, err := s.svc.UnclaimTask(r.Context(), actor, chi.URLParam(r, "taskID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *Server) handleAssignTask(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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
		EnforceFreshness: actor.Kind == domain.MemberKindAgent,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeTaskWriteResult(w, res)
}

func (s *Server) handleUpdateTaskStatus(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleAmendTask(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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
