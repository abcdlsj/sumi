package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

type CreateTaskInput struct {
	ChannelID        string
	SourceMessageID  string // Optional; non-unique (one message can spawn multiple tasks).
	Title            string
	Description      string
	AssigneeMemberID string // Optional; Human or Agent.
	Status           domain.TaskStatus
}

func (s *Service) CreateTask(ctx context.Context, actor domain.Member, in CreateTaskInput) (domain.Task, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return domain.Task{}, domain.NewError(domain.CodeInvalidArgument, "task title is required")
	}
	status := in.Status
	if status == "" {
		status = domain.TaskTodo
	}
	if status != domain.TaskTodo && status != domain.TaskInProgress {
		return domain.Task{}, domain.NewError(domain.CodeInvalidArgument, "initial task status must be todo or in_progress")
	}

	var task domain.Task
	var events []domain.SpaceEvent
	var wakes []wakeTarget

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		chMeta, err := canAccessConversation(ctx, tx, actor, in.ChannelID)
		if err != nil {
			return err
		}
		if chMeta.Kind == domain.ConversationThread || chMeta.Kind == domain.ConversationTask {
			return domain.NewError(domain.CodeInvalidArgument, "task channel_id must be a channel or dm")
		}

		if in.SourceMessageID != "" {
			var srcConv string
			if err := tx.QueryRowContext(ctx, `SELECT conversation_id FROM messages WHERE id = ?`, in.SourceMessageID).Scan(&srcConv); err != nil {
				return domain.NewError(domain.CodeNotFound, "source_message_id not found")
			}
			if srcConv != chMeta.ID {
				return domain.NewError(domain.CodeInvalidArgument, "source_message_id does not belong to channel_id")
			}
		}

		var assignee *domain.Member
		if in.AssigneeMemberID != "" {
			m, err := loadMember(ctx, tx, in.AssigneeMemberID)
			if err != nil || m.SpaceID != actor.SpaceID || m.RetiredAt != nil {
				return domain.NewError(domain.CodeNotFound, "assignee member not found")
			}
			assignee = &m
		}

		var num int64
		if err := tx.QueryRowContext(ctx,
			`UPDATE spaces SET next_task_number = next_task_number + 1 WHERE id = ? RETURNING next_task_number - 1`,
			actor.SpaceID,
		).Scan(&num); err != nil {
			return domain.WrapError(domain.CodeInternal, "allocate task number", err)
		}

		now := time.Now().UTC()
		taskID := uuid.NewString()
		var srcMsgArg, assigneeArg any
		if in.SourceMessageID != "" {
			srcMsgArg = in.SourceMessageID
		}
		if assignee != nil {
			assigneeArg = assignee.ID
		}

		// Task conversation_id stays NULL until lazily created on first write/event.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tasks (id, space_id, number, channel_id, source_message_id, conversation_id, title, description, status, creator_member_id, assignee_member_id, revision, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?, 1, ?, ?)`,
			taskID, actor.SpaceID, num, chMeta.ID, srcMsgArg, title, strings.TrimSpace(in.Description), string(status), actor.ID, assigneeArg, store.FormatTime(now), store.FormatTime(now),
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "insert task", err)
		}

		ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "task.created", map[string]any{
			"task_id":           taskID,
			"number":            num,
			"channel_id":        chMeta.ID,
			"source_message_id": in.SourceMessageID,
		})
		if err != nil {
			return err
		}
		events = append(events, ev)

		task = domain.Task{
			ID:              taskID,
			SpaceID:         actor.SpaceID,
			Number:          num,
			ChannelID:       chMeta.ID,
			ChannelSlug:     chMeta.Slug,
			SourceMessageID: in.SourceMessageID,
			Title:           title,
			Description:     strings.TrimSpace(in.Description),
			Status:          status,
			CreatorMemberID: actor.ID,
			Revision:        1,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if assignee != nil {
			task.AssigneeMemberID = assignee.ID
			task.AssigneeName = assignee.DisplayName
			task.AssigneeKind = assignee.Kind
			// If assigned to an agent other than creator, lazily create task conversation and wake them.
			if assignee.Kind == domain.MemberKindAgent && assignee.ID != actor.ID {
				taskConv, _, err := ensureTaskConversationTx(ctx, tx, &task)
				if err != nil {
					return err
				}
				_ = setFollowingTx(ctx, tx, taskConv.ID, actor.ID, true)
				_ = setFollowingTx(ctx, tx, taskConv.ID, assignee.ID, true)
				_, ev2, err := insertActionMessageTx(ctx, tx, actor, taskConv.ID, domain.ActionTaskEvent, map[string]any{
					"op":            "assigned",
					"task_number":   task.Number,
					"assignee_id":   assignee.ID,
					"assignee_name": assignee.DisplayName,
					"status":        task.Status,
				})
				if err != nil {
					return err
				}
				events = append(events, ev2)
				if w, ok := agentWakeForMemberTx(ctx, tx, assignee.ID, taskConv.ID, "task_assigned"); ok {
					wakes = append(wakes, w)
				}
			}
		}
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	s.dispatchPostCommit(events, wakes)
	return task, nil
}

func loadTaskTx(ctx context.Context, q queryer, spaceID, taskID string) (domain.Task, error) {
	var t domain.Task
	var status, aKind, createdAt, updatedAt string
	err := q.QueryRowContext(ctx,
		`SELECT t.id, t.space_id, t.number, t.channel_id, COALESCE(c.slug, ''), COALESCE(t.source_message_id, ''),
		        COALESCE(t.conversation_id, ''), t.title, t.description, t.status, t.creator_member_id,
		        COALESCE(t.assignee_member_id, ''), COALESCE(am.display_name, ''), COALESCE(am.kind, ''),
		        t.revision,
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = t.conversation_id AND m.deleted_at IS NULL), 0),
		        t.created_at, t.updated_at
		 FROM tasks t
		 JOIN conversations c ON c.id = t.channel_id
		 LEFT JOIN members am ON am.id = t.assignee_member_id
		 WHERE t.id = ? AND t.space_id = ?`,
		taskID, spaceID,
	).Scan(
		&t.ID, &t.SpaceID, &t.Number, &t.ChannelID, &t.ChannelSlug, &t.SourceMessageID,
		&t.ConversationID, &t.Title, &t.Description, &status, &t.CreatorMemberID,
		&t.AssigneeMemberID, &t.AssigneeName, &aKind, &t.Revision, &t.MessageCount,
		&createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.NewError(domain.CodeNotFound, "task not found")
	}
	if err != nil {
		return domain.Task{}, domain.WrapError(domain.CodeInternal, "query task", err)
	}
	t.Status = domain.TaskStatus(status)
	t.AssigneeKind = domain.MemberKind(aKind)
	t.CreatedAt = store.ParseTime(createdAt)
	t.UpdatedAt = store.ParseTime(updatedAt)
	return t, nil
}

func ensureTaskConversationTx(ctx context.Context, tx *sql.Tx, task *domain.Task) (convMeta, bool, error) {
	if task.ConversationID != "" {
		c, err := loadConvMeta(ctx, tx, task.ConversationID)
		return c, false, err
	}
	now := time.Now().UTC()
	convID := uuid.NewString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO conversations (id, space_id, kind, parent_conversation_id, task_id, created_at)
		 VALUES (?, ?, 'task', ?, ?, ?)`,
		convID, task.SpaceID, task.ChannelID, task.ID, store.FormatTime(now),
	); err != nil {
		return convMeta{}, false, domain.WrapError(domain.CodeInternal, "create task conversation", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET conversation_id = ? WHERE id = ?`, convID, task.ID); err != nil {
		return convMeta{}, false, domain.WrapError(domain.CodeInternal, "link task conversation", err)
	}
	task.ConversationID = convID
	return convMeta{
		ID:                   convID,
		SpaceID:              task.SpaceID,
		Kind:                 domain.ConversationTask,
		ParentConversationID: task.ChannelID,
		TaskID:               task.ID,
		NextSeq:              1,
	}, true, nil
}

// EnsureTaskConversation lazily materializes the task's own conversation when the first reply arrives.
func (s *Service) EnsureTaskConversation(ctx context.Context, actor domain.Member, taskID string) (domain.Task, error) {
	var task domain.Task
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTaskTx(ctx, tx, actor.SpaceID, taskID)
		if err != nil {
			return err
		}
		if _, err := canAccessConversation(ctx, tx, actor, t.ChannelID); err != nil {
			return err
		}
		conv, created, err := ensureTaskConversationTx(ctx, tx, &t)
		if err != nil {
			return err
		}
		if created {
			ev, err := store.AppendEvent(ctx, tx, actor.SpaceID, "conversation.created", map[string]any{
				"conversation_id":        conv.ID,
				"kind":                   "task",
				"parent_conversation_id": conv.ParentConversationID,
				"task_id":                t.ID,
			})
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		task = t
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	s.dispatchPostCommit(events, nil)
	return task, nil
}

func (s *Service) GetTask(ctx context.Context, viewer domain.Member, taskID string) (domain.Task, error) {
	t, err := loadTaskTx(ctx, s.store.ReadDB(), viewer.SpaceID, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if _, err := canAccessConversation(ctx, s.store.ReadDB(), viewer, t.ChannelID); err != nil {
		return domain.Task{}, err
	}
	return t, nil
}

func (s *Service) GetTaskByNumber(ctx context.Context, viewer domain.Member, number int64) (domain.Task, error) {
	var id string
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE space_id = ? AND number = ?`,
		viewer.SpaceID, number,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, domain.NewError(domain.CodeNotFound, "task not found")
	}
	if err != nil {
		return domain.Task{}, domain.WrapError(domain.CodeInternal, "query task by number", err)
	}
	return s.GetTask(ctx, viewer, id)
}

func (s *Service) ListTasks(ctx context.Context, viewer domain.Member, statusFilter, assigneeFilter string) ([]domain.Task, error) {
	rows, err := s.store.ReadDB().QueryContext(ctx,
		`SELECT t.id, t.space_id, t.number, t.channel_id, COALESCE(c.slug, ''), COALESCE(t.source_message_id, ''),
		        COALESCE(t.conversation_id, ''), t.title, t.description, t.status, t.creator_member_id,
		        COALESCE(t.assignee_member_id, ''), COALESCE(am.display_name, ''), COALESCE(am.kind, ''),
		        t.revision,
		        COALESCE((SELECT COUNT(*) FROM messages m WHERE m.conversation_id = t.conversation_id AND m.deleted_at IS NULL), 0),
		        t.created_at, t.updated_at
		 FROM tasks t
		 JOIN conversations c ON c.id = t.channel_id
		 LEFT JOIN conversation_members cm ON cm.conversation_id = c.id AND cm.member_id = ?
		 LEFT JOIN members am ON am.id = t.assignee_member_id
		 WHERE t.space_id = ?
		   AND ((c.kind = 'public' AND ? = 'human') OR cm.member_id IS NOT NULL)
		 ORDER BY CASE t.status WHEN 'in_review' THEN 0 WHEN 'in_progress' THEN 1 WHEN 'todo' THEN 2 WHEN 'done' THEN 3 ELSE 4 END, t.updated_at DESC`,
		viewer.ID, viewer.SpaceID, string(viewer.Kind),
	)
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "list tasks", err)
	}
	defer rows.Close()

	out := make([]domain.Task, 0)
	for rows.Next() {
		var t domain.Task
		var status, aKind, createdAt, updatedAt string
		if err := rows.Scan(
			&t.ID, &t.SpaceID, &t.Number, &t.ChannelID, &t.ChannelSlug, &t.SourceMessageID,
			&t.ConversationID, &t.Title, &t.Description, &status, &t.CreatorMemberID,
			&t.AssigneeMemberID, &t.AssigneeName, &aKind, &t.Revision, &t.MessageCount,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		t.Status = domain.TaskStatus(status)
		t.AssigneeKind = domain.MemberKind(aKind)
		t.CreatedAt = store.ParseTime(createdAt)
		t.UpdatedAt = store.ParseTime(updatedAt)

		if statusFilter == "active" && (t.Status == domain.TaskDone || t.Status == domain.TaskClosed) {
			continue
		} else if statusFilter != "" && statusFilter != "all" && statusFilter != "active" && string(t.Status) != statusFilter {
			continue
		}
		if assigneeFilter == "mine" && t.AssigneeMemberID != viewer.ID {
			continue
		} else if assigneeFilter != "" && assigneeFilter != "mine" && t.AssigneeMemberID != assigneeFilter {
			continue
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type TaskWriteResult struct {
	Task           domain.Task      `json:"task,omitempty"`
	Held           bool             `json:"held,omitempty"`
	UnreadMessages []domain.Message `json:"unread_messages,omitempty"`
	// OmittedUnreadCount counts older unread messages not included in UnreadMessages.
	OmittedUnreadCount int64 `json:"omitted_unread_count,omitempty"`
}

func (s *Service) ClaimTask(ctx context.Context, actor domain.Member, taskID string, enforceFreshness bool) (TaskWriteResult, error) {
	var res TaskWriteResult
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTaskTx(ctx, tx, actor.SpaceID, taskID)
		if err != nil {
			return err
		}
		if _, err := canAccessConversation(ctx, tx, actor, t.ChannelID); err != nil {
			return err
		}
		if t.Status == domain.TaskDone {
			return domain.NewError(domain.CodeConflict, "task is done; reopen it before claiming")
		}
		if t.Status == domain.TaskClosed {
			return domain.NewError(domain.CodeConflict, "task is closed; reopen it before claiming")
		}
		if t.AssigneeMemberID != "" && t.AssigneeMemberID != actor.ID {
			return domain.NewError(domain.CodeConflict, "task is already claimed by another member")
		}
		if enforceFreshness && t.ConversationID != "" {
			held, err := checkFreshnessTx(ctx, tx, t.ConversationID, actor.ID)
			if err != nil {
				return err
			}
			if held != nil {
				res = TaskWriteResult{Held: true, UnreadMessages: held.Messages, OmittedUnreadCount: held.Omitted}
				return nil
			}
		}

		now := time.Now().UTC()
		newStatus := t.Status
		if newStatus == domain.TaskTodo {
			newStatus = domain.TaskInProgress
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET assignee_member_id = ?, status = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			actor.ID, string(newStatus), store.FormatTime(now), t.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "claim task", err)
		}
		t.AssigneeMemberID = actor.ID
		t.AssigneeName = actor.DisplayName
		t.AssigneeKind = actor.Kind
		t.Status = newStatus
		t.Revision++
		t.UpdatedAt = now

		taskConv, _, err := ensureTaskConversationTx(ctx, tx, &t)
		if err != nil {
			return err
		}
		_ = setFollowingTx(ctx, tx, taskConv.ID, actor.ID, true)
		msg, evMsg, err := insertActionMessageTx(ctx, tx, actor, taskConv.ID, domain.ActionTaskEvent, map[string]any{
			"op":          "claimed",
			"task_number": t.Number,
			"status":      t.Status,
		})
		if err != nil {
			return err
		}
		_ = advanceReadSeqTx(ctx, tx, taskConv.ID, actor.ID, msg.Seq)
		events = append(events, evMsg)

		evTask, err := store.AppendEvent(ctx, tx, actor.SpaceID, "task.updated", map[string]any{
			"task_id": t.ID,
			"number":  t.Number,
			"status":  t.Status,
		})
		if err != nil {
			return err
		}
		events = append(events, evTask)
		t.MessageCount++
		res = TaskWriteResult{Task: t}
		return nil
	})
	if err != nil {
		return TaskWriteResult{}, err
	}
	if !res.Held {
		s.dispatchPostCommit(events, nil)
	}
	return res, nil
}

func (s *Service) UnclaimTask(ctx context.Context, actor domain.Member, taskID string) (domain.Task, error) {
	var updated domain.Task
	var events []domain.SpaceEvent
	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTaskTx(ctx, tx, actor.SpaceID, taskID)
		if err != nil {
			return err
		}
		if _, err := canAccessConversation(ctx, tx, actor, t.ChannelID); err != nil {
			return err
		}
		now := time.Now().UTC()
		newStatus := t.Status
		if newStatus == domain.TaskInProgress {
			newStatus = domain.TaskTodo
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET assignee_member_id = NULL, status = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			string(newStatus), store.FormatTime(now), t.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "unclaim task", err)
		}
		t.AssigneeMemberID = ""
		t.AssigneeName = ""
		t.AssigneeKind = ""
		t.Status = newStatus
		t.Revision++
		t.UpdatedAt = now

		taskConv, _, err := ensureTaskConversationTx(ctx, tx, &t)
		if err != nil {
			return err
		}
		msg, evMsg, err := insertActionMessageTx(ctx, tx, actor, taskConv.ID, domain.ActionTaskEvent, map[string]any{
			"op":          "unclaimed",
			"task_number": t.Number,
			"status":      t.Status,
		})
		if err != nil {
			return err
		}
		_ = advanceReadSeqTx(ctx, tx, taskConv.ID, actor.ID, msg.Seq)
		events = append(events, evMsg)

		evTask, err := store.AppendEvent(ctx, tx, actor.SpaceID, "task.updated", map[string]any{
			"task_id": t.ID,
			"number":  t.Number,
			"status":  t.Status,
		})
		if err != nil {
			return err
		}
		events = append(events, evTask)
		t.MessageCount++
		updated = t
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	s.dispatchPostCommit(events, nil)
	return updated, nil
}

type AssignTaskInput struct {
	TaskID           string
	AssigneeMemberID string // Empty means unassign.
	Reason           string // Required when an Agent assigns to another member.
	ExpectedRevision int64  // Optional CAS guard.
	EnforceFreshness bool
}

func (s *Service) AssignTask(ctx context.Context, actor domain.Member, in AssignTaskInput) (TaskWriteResult, error) {
	reason := strings.TrimSpace(in.Reason)
	if actor.Kind == domain.MemberKindAgent && in.AssigneeMemberID != "" && in.AssigneeMemberID != actor.ID && reason == "" {
		return TaskWriteResult{}, domain.NewError(domain.CodeInvalidArgument, "agent must provide a one-line reason when assigning a task to someone else")
	}

	var res TaskWriteResult
	var events []domain.SpaceEvent
	var wakes []wakeTarget

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTaskTx(ctx, tx, actor.SpaceID, in.TaskID)
		if err != nil {
			return err
		}
		if _, err := canAccessConversation(ctx, tx, actor, t.ChannelID); err != nil {
			return err
		}
		if in.ExpectedRevision > 0 && t.Revision != in.ExpectedRevision {
			return domain.NewError(domain.CodeConflict, "task revision changed; reread before assigning")
		}
		if in.EnforceFreshness && t.ConversationID != "" {
			held, err := checkFreshnessTx(ctx, tx, t.ConversationID, actor.ID)
			if err != nil {
				return err
			}
			if held != nil {
				res = TaskWriteResult{Held: true, UnreadMessages: held.Messages, OmittedUnreadCount: held.Omitted}
				return nil
			}
		}

		var assignee *domain.Member
		var assigneeArg any
		if in.AssigneeMemberID != "" {
			m, err := loadMember(ctx, tx, in.AssigneeMemberID)
			if err != nil || m.SpaceID != actor.SpaceID || m.RetiredAt != nil {
				return domain.NewError(domain.CodeNotFound, "assignee member not found")
			}
			assignee = &m
			assigneeArg = m.ID
		}

		now := time.Now().UTC()
		// Assign does NOT change status.
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET assignee_member_id = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			assigneeArg, store.FormatTime(now), t.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "assign task", err)
		}
		t.Revision++
		t.UpdatedAt = now
		if assignee != nil {
			t.AssigneeMemberID = assignee.ID
			t.AssigneeName = assignee.DisplayName
			t.AssigneeKind = assignee.Kind
		} else {
			t.AssigneeMemberID = ""
			t.AssigneeName = ""
			t.AssigneeKind = ""
		}

		taskConv, _, err := ensureTaskConversationTx(ctx, tx, &t)
		if err != nil {
			return err
		}
		_ = setFollowingTx(ctx, tx, taskConv.ID, actor.ID, true)
		if assignee != nil {
			_ = setFollowingTx(ctx, tx, taskConv.ID, assignee.ID, true)
		}

		payload := map[string]any{
			"op":          "assigned",
			"task_number": t.Number,
			"reason":      reason,
		}
		if assignee != nil {
			payload["assignee_id"] = assignee.ID
			payload["assignee_name"] = assignee.DisplayName
		}
		msg, evMsg, err := insertActionMessageTx(ctx, tx, actor, taskConv.ID, domain.ActionTaskEvent, payload)
		if err != nil {
			return err
		}
		_ = advanceReadSeqTx(ctx, tx, taskConv.ID, actor.ID, msg.Seq)
		events = append(events, evMsg)

		evTask, err := store.AppendEvent(ctx, tx, actor.SpaceID, "task.updated", map[string]any{
			"task_id": t.ID,
			"number":  t.Number,
		})
		if err != nil {
			return err
		}
		events = append(events, evTask)

		if assignee != nil && assignee.Kind == domain.MemberKindAgent && assignee.ID != actor.ID {
			if w, ok := agentWakeForMemberTx(ctx, tx, assignee.ID, taskConv.ID, "task_assigned"); ok {
				wakes = append(wakes, w)
			}
		}
		t.MessageCount++
		res = TaskWriteResult{Task: t}
		return nil
	})
	if err != nil {
		return TaskWriteResult{}, err
	}
	if !res.Held {
		s.dispatchPostCommit(events, wakes)
	}
	return res, nil
}

func (s *Service) UpdateTaskStatus(ctx context.Context, actor domain.Member, taskID string, status domain.TaskStatus) (domain.Task, error) {
	if err := domain.ValidateTaskStatus(status); err != nil {
		return domain.Task{}, err
	}
	var updated domain.Task
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTaskTx(ctx, tx, actor.SpaceID, taskID)
		if err != nil {
			return err
		}
		if _, err := canAccessConversation(ctx, tx, actor, t.ChannelID); err != nil {
			return err
		}
		if t.Status == status {
			updated = t
			return nil
		}
		prev := t.Status
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			string(status), store.FormatTime(now), t.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "update task status", err)
		}
		t.Status = status
		t.Revision++
		t.UpdatedAt = now

		taskConv, _, err := ensureTaskConversationTx(ctx, tx, &t)
		if err != nil {
			return err
		}
		msg, evMsg, err := insertActionMessageTx(ctx, tx, actor, taskConv.ID, domain.ActionTaskEvent, map[string]any{
			"op":          "status_changed",
			"task_number": t.Number,
			"from":        prev,
			"to":          status,
		})
		if err != nil {
			return err
		}
		_ = advanceReadSeqTx(ctx, tx, taskConv.ID, actor.ID, msg.Seq)
		events = append(events, evMsg)

		evTask, err := store.AppendEvent(ctx, tx, actor.SpaceID, "task.updated", map[string]any{
			"task_id": t.ID,
			"number":  t.Number,
			"status":  t.Status,
		})
		if err != nil {
			return err
		}
		events = append(events, evTask)
		t.MessageCount++
		updated = t
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	s.dispatchPostCommit(events, nil)
	return updated, nil
}

type AmendTaskInput struct {
	TaskID           string
	Title            *string
	Description      *string
	ExpectedRevision int64
}

func (s *Service) AmendTask(ctx context.Context, actor domain.Member, in AmendTaskInput) (domain.Task, error) {
	var updated domain.Task
	var events []domain.SpaceEvent

	err := s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTaskTx(ctx, tx, actor.SpaceID, in.TaskID)
		if err != nil {
			return err
		}
		if _, err := canAccessConversation(ctx, tx, actor, t.ChannelID); err != nil {
			return err
		}
		if in.ExpectedRevision > 0 && t.Revision != in.ExpectedRevision {
			return domain.NewError(domain.CodeConflict, "task revision mismatch")
		}
		newTitle := t.Title
		newDesc := t.Description
		if in.Title != nil {
			v := strings.TrimSpace(*in.Title)
			if v == "" {
				return domain.NewError(domain.CodeInvalidArgument, "title cannot be empty")
			}
			newTitle = v
		}
		if in.Description != nil {
			newDesc = strings.TrimSpace(*in.Description)
		}
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET title = ?, description = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			newTitle, newDesc, store.FormatTime(now), t.ID,
		); err != nil {
			return domain.WrapError(domain.CodeInternal, "amend task", err)
		}
		t.Title = newTitle
		t.Description = newDesc
		t.Revision++
		t.UpdatedAt = now

		taskConv, _, err := ensureTaskConversationTx(ctx, tx, &t)
		if err != nil {
			return err
		}
		msg, evMsg, err := insertActionMessageTx(ctx, tx, actor, taskConv.ID, domain.ActionTaskEvent, map[string]any{
			"op":          "amended",
			"task_number": t.Number,
			"revision":    t.Revision,
		})
		if err != nil {
			return err
		}
		_ = advanceReadSeqTx(ctx, tx, taskConv.ID, actor.ID, msg.Seq)
		events = append(events, evMsg)

		evTask, err := store.AppendEvent(ctx, tx, actor.SpaceID, "task.updated", map[string]any{
			"task_id": t.ID,
			"number":  t.Number,
		})
		if err != nil {
			return err
		}
		events = append(events, evTask)
		t.MessageCount++
		updated = t
		return nil
	})
	if err != nil {
		return domain.Task{}, err
	}
	s.dispatchPostCommit(events, nil)
	return updated, nil
}

func agentWakeForMemberTx(ctx context.Context, tx *sql.Tx, agentMemberID, convID, reason string) (wakeTarget, bool) {
	var compID string
	err := tx.QueryRowContext(ctx,
		`SELECT a.computer_id FROM agents a JOIN computers c ON c.id = a.computer_id
		 WHERE a.member_id = ? AND c.connection_status = 'online'`,
		agentMemberID,
	).Scan(&compID)
	if err != nil || compID == "" {
		return wakeTarget{}, false
	}
	return wakeTarget{
		computerID:     compID,
		agentMemberID:  agentMemberID,
		conversationID: convID,
		reason:         reason,
	}, true
}
