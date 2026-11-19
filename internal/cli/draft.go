package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// draft is a held send kept on disk so the Agent can resend it after reading, without retyping
// or re-uploading. It lives under the Agent's own draft dir and never enters logs.
type draft struct {
	ConversationID   string   `json:"conversation_id"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	BodyMarkdown     string   `json:"body_markdown"`
	AttachmentIDs    []string `json:"attachment_ids,omitempty"`
	IdempotencyKey   string   `json:"idempotency_key"`
}

func draftDir() string {
	if d := os.Getenv("SUMI_DRAFT_DIR"); d != "" {
		return d
	}
	return ".sumi-drafts"
}

func draftPath(convID string) (string, error) {
	if convID == "" || strings.ContainsAny(convID, `/\`) || strings.HasPrefix(convID, ".") {
		return "", errors.New("invalid conversation id")
	}
	return filepath.Join(draftDir(), convID+".json"), nil
}

func saveDraft(d draft) error {
	path, err := draftPath(d.ConversationID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.Marshal(d)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadDraft(convID string) (draft, error) {
	path, err := draftPath(convID)
	if err != nil {
		return draft{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return draft{}, errors.New("no saved draft for this conversation")
		}
		return draft{}, err
	}
	var d draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return draft{}, err
	}
	return d, nil
}

func deleteDraft(convID string) {
	if path, err := draftPath(convID); err == nil {
		_ = os.Remove(path)
	}
}

// sendKey derives a stable Idempotency-Key from the logical message, so re-running the same
// send after a lost response replays instead of posting twice.
func sendKey(convID, replyTo, body string, attachPaths []string) (string, error) {
	h := sha256.New()
	for _, part := range []string{convID, replyTo, strings.TrimSpace(body)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	for _, p := range attachPaths {
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return "cli-" + hex.EncodeToString(h.Sum(nil))[:40], nil
}
