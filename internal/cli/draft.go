package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// draft is a held send kept on disk so the Agent can resend it after reading, without retyping
// or re-uploading. It lives under the Agent's own draft dir and never enters logs.
type draft struct {
	ConversationID   string    `json:"conversation_id"`
	ReplyToMessageID string    `json:"reply_to_message_id,omitempty"`
	BodyMarkdown     string    `json:"body_markdown"`
	AttachmentIDs    []string  `json:"attachment_ids,omitempty"`
	IdempotencyKey   string    `json:"idempotency_key"`
	SavedAt          time.Time `json:"saved_at"`
}

const (
	// draftTTL bounds how long a held send stays resendable; older context is stale anyway.
	draftTTL = 10 * time.Minute
	// keepReplaced is how many replaced drafts per conversation are kept under replaced/.
	keepReplaced = 5
)

var errNoDraft = errors.New("no saved draft for this conversation")

func (d draft) expired(now time.Time) bool { return now.Sub(d.SavedAt) > draftTTL }

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

// saveDraft stores d as the conversation's draft. A different, unexpired draft already there is
// moved to replaced/ first and its path returned, so a rewrite never silently loses text.
func saveDraft(d draft) (string, error) {
	path, err := draftPath(d.ConversationID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if d.SavedAt.IsZero() {
		d.SavedAt = time.Now().UTC()
	}
	replaced := ""
	if old, err := loadDraft(d.ConversationID); err == nil && old.IdempotencyKey != d.IdempotencyKey && !old.expired(time.Now()) {
		if replaced, err = archiveDraft(old); err != nil {
			return "", err
		}
	}
	raw, _ := json.Marshal(d)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	return replaced, os.Rename(tmp, path)
}

func archiveDraft(d draft) (string, error) {
	dir := filepath.Join(draftDir(), "replaced")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(d)
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", d.ConversationID, time.Now().UnixNano()))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, d.ConversationID+"-*.json"))
	sort.Strings(matches) // Names end in a fixed-width nanosecond timestamp, so this is oldest first.
	for len(matches) > keepReplaced {
		_ = os.Remove(matches[0])
		matches = matches[1:]
	}
	return path, nil
}

func loadDraft(convID string) (draft, error) {
	path, err := draftPath(convID)
	if err != nil {
		return draft{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return draft{}, errNoDraft
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

// settleDraftAfterSend runs after a successful send: the draft is deleted only when this send
// was that draft (same Idempotency-Key) or it has expired. It reports whether one is still pending.
func settleDraftAfterSend(convID, sentKey string) bool {
	d, err := loadDraft(convID)
	if err != nil {
		return false
	}
	if d.IdempotencyKey == sentKey || d.expired(time.Now()) {
		deleteDraft(convID)
		return false
	}
	return true
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
