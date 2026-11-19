package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Target addressing a Conversation: `#channel`, `dm:@name`, `thread:<id>`, `task:#N`, or a bare
// conversation id. The Server API only knows conversation ids, so the CLI resolves targets itself.
const (
	targetChannel = "#"
	targetDM      = "dm:"
	targetThread  = "thread:"
	targetTask    = "task:#"
)

type conversationRef struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Slug            string `json:"slug"`
	PeerMemberID    string `json:"peer_member_id"`
	PeerDisplayName string `json:"peer_display_name"`
}

type taskRef struct {
	ID             string `json:"id"`
	Number         int64  `json:"number"`
	ConversationID string `json:"conversation_id"`
}

// resolveTarget turns a user-facing target into a conversation id.
func resolveTarget(c *Client, target string) (string, *EnvelopeError) {
	raw := strings.TrimSpace(target)
	switch {
	case raw == "":
		return "", localError(codeInvalidArgument, fmt.Errorf("target is required"))
	case strings.HasPrefix(raw, targetChannel):
		return resolveChannel(c, strings.TrimSpace(strings.TrimPrefix(raw, targetChannel)))
	case strings.HasPrefix(raw, targetDM):
		return resolveDM(c, strings.TrimSpace(strings.TrimPrefix(raw, targetDM)))
	case strings.HasPrefix(raw, targetThread):
		id := strings.TrimSpace(strings.TrimPrefix(raw, targetThread))
		if id == "" {
			return "", localError(codeInvalidArgument, fmt.Errorf("thread target needs a conversation id: `thread:<id>`"))
		}
		return id, nil
	case strings.HasPrefix(raw, targetTask):
		return resolveTask(c, strings.TrimPrefix(raw, targetTask))
	}
	return raw, nil
}

func resolveChannel(c *Client, slug string) (string, *EnvelopeError) {
	if slug == "" {
		return "", localError(codeInvalidArgument, fmt.Errorf("channel target needs a slug: `#channel`"))
	}
	convs, e := c.listConversations()
	if e != nil {
		return "", e
	}
	for _, conv := range convs {
		if (conv.Kind == "public" || conv.Kind == "private") && strings.EqualFold(conv.Slug, slug) {
			return conv.ID, nil
		}
	}
	return "", targetNotFound(fmt.Sprintf("no channel `#%s` among your conversations", slug))
}

func resolveDM(c *Client, peer string) (string, *EnvelopeError) {
	name := strings.TrimSpace(strings.TrimPrefix(peer, "@"))
	if name == "" {
		return "", localError(codeInvalidArgument, fmt.Errorf("dm target needs a name: `dm:@name`"))
	}
	convs, e := c.listConversations()
	if e != nil {
		return "", e
	}
	for _, conv := range convs {
		if conv.Kind != "dm" {
			continue
		}
		if strings.EqualFold(conv.PeerDisplayName, name) || conv.PeerMemberID == name {
			return conv.ID, nil
		}
	}
	return "", targetNotFound(fmt.Sprintf("no direct message with @%s", name))
}

func resolveTask(c *Client, digits string) (string, *EnvelopeError) {
	number, err := strconv.ParseInt(strings.TrimSpace(digits), 10, 64)
	if err != nil || number <= 0 {
		return "", localError(codeInvalidArgument, fmt.Errorf("task target needs a task number: `task:#N`"))
	}
	status, raw, err := c.Do(http.MethodGet, "/tasks?status=all", nil)
	if err != nil {
		return "", localError(codeUnavailable, err)
	}
	if e, _ := responseError(status, raw); e != nil {
		return "", e
	}
	var parsed struct {
		Tasks []taskRef `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", localError(codeInternal, err)
	}
	for _, task := range parsed.Tasks {
		if task.Number != number {
			continue
		}
		if task.ConversationID != "" {
			return task.ConversationID, nil
		}
		return materializeTaskConversation(c, task.ID)
	}
	return "", targetNotFound(fmt.Sprintf("no task #%d in this space", number))
}

// materializeTaskConversation asks the Server to lazily create the task's own conversation.
func materializeTaskConversation(c *Client, taskID string) (string, *EnvelopeError) {
	status, raw, err := c.Do(http.MethodPost, "/tasks/"+taskID+"/conversation", nil)
	if err != nil {
		return "", localError(codeUnavailable, err)
	}
	if e, _ := responseError(status, raw); e != nil {
		return "", e
	}
	var parsed struct {
		Task taskRef `json:"task"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", localError(codeInternal, err)
	}
	if parsed.Task.ConversationID == "" {
		return "", localError(codeInternal, fmt.Errorf("task #%d has no conversation", parsed.Task.Number))
	}
	return parsed.Task.ConversationID, nil
}

func (c *Client) listConversations() ([]conversationRef, *EnvelopeError) {
	status, raw, err := c.Do(http.MethodGet, "/conversations", nil)
	if err != nil {
		return nil, localError(codeUnavailable, err)
	}
	if e, _ := responseError(status, raw); e != nil {
		return nil, e
	}
	var parsed struct {
		Conversations []conversationRef `json:"conversations"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, localError(codeInternal, err)
	}
	return parsed.Conversations, nil
}

func targetNotFound(message string) *EnvelopeError {
	e := localError("not_found", fmt.Errorf("%s", message))
	e.Hint = hintFor("not_found")
	return e
}
