package server

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
)

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	convs, err := s.svc.ListConversations(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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
	conv, err := s.svc.CreateChannel(r.Context(), actor, app.CreateChannelInput{
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

func (s *Server) handleListArchivedChannels(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	convs, err := s.svc.ListArchivedChannels(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": convs})
}

func (s *Server) handleArchiveChannel(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	conv, err := s.svc.ArchiveChannel(r.Context(), actor, chi.URLParam(r, "convID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleUnarchiveChannel(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	conv, err := s.svc.UnarchiveChannel(r.Context(), actor, chi.URLParam(r, "convID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleCreateChannelGroup(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	g, err := s.svc.CreateChannelGroup(r.Context(), actor, body.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"group": g})
}

func (s *Server) handleUpdateChannelGroup(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	var body struct {
		Name      *string `json:"name"`
		Collapsed *bool   `json:"collapsed"`
		Position  *int    `json:"position"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	g, err := s.svc.UpdateChannelGroup(r.Context(), actor, chi.URLParam(r, "groupID"), app.UpdateChannelGroupInput{
		Name:      body.Name,
		Collapsed: body.Collapsed,
		Position:  body.Position,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": g})
}

func (s *Server) handleDeleteChannelGroup(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	if err := s.svc.DeleteChannelGroup(r.Context(), actor, chi.URLParam(r, "groupID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetConversationGroup(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	var body struct {
		GroupID string `json:"group_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := s.svc.SetConversationGroup(r.Context(), actor, chi.URLParam(r, "convID"), body.GroupID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCreateDM(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleListConversationMembers(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	members, err := s.svc.ListConversationMembers(r.Context(), actor, chi.URLParam(r, "convID"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": members})
}

func (s *Server) handleInviteToChannel(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	var body struct {
		MemberID string `json:"member_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, err)
		return
	}
	if err := s.svc.InviteToChannel(r.Context(), actor, chi.URLParam(r, "convID"), body.MemberID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRemoveFromChannel(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	if err := s.svc.RemoveFromChannel(r.Context(), actor, chi.URLParam(r, "convID"), chi.URLParam(r, "memberID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	beforeSeq, _ := strconv.ParseInt(r.URL.Query().Get("before_seq"), 10, 64)
	latest := r.URL.Query().Get("latest") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	markRead := r.URL.Query().Get("mark_read") == "true"
	page, err := s.svc.ListMessages(r.Context(), actor, app.ListMessagesInput{
		ConversationID: chi.URLParam(r, "convID"),
		AfterSeq:       afterSeq,
		BeforeSeq:      beforeSeq,
		Latest:         latest,
		Limit:          limit,
		MarkRead:       markRead,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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
		EnforceFreshness: actor.Kind == domain.MemberKindAgent,
		IdempotencyKey:   r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeSendResult(w, res)
}

func (s *Server) handleAddReaction(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	reactions, err := s.svc.AddReaction(r.Context(), actor, chi.URLParam(r, "messageID"), chi.URLParam(r, "emoji"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reactions": reactions})
}

func (s *Server) handleRemoveReaction(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	reactions, err := s.svc.RemoveReaction(r.Context(), actor, chi.URLParam(r, "messageID"), chi.URLParam(r, "emoji"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reactions": reactions})
}

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleMarkDone(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	if err := s.svc.MarkDone(r.Context(), actor, chi.URLParam(r, "convID")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetNotifyLevel(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleSetFollowing(w http.ResponseWriter, r *http.Request, actor domain.Member) {
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

func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := s.svc.SearchMessages(r.Context(), actor, app.SearchMessagesInput{
		Query:          r.URL.Query().Get("q"),
		ConversationID: r.URL.Query().Get("conversation_id"),
		Limit:          limit,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (s *Server) handleListInbox(w http.ResponseWriter, r *http.Request, actor domain.Member) {
	filter := r.URL.Query().Get("filter")
	if filter == "" && actor.Kind == domain.MemberKindAgent {
		filter = "needs_me"
	}
	items, err := s.svc.ListInbox(r.Context(), actor, filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
