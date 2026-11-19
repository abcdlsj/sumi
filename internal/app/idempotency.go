package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

const (
	actionMessageSend = "message.send"
	// idempotencyWindow bounds how long a key replays; afterwards the same key sends anew.
	idempotencyWindow = 10 * time.Minute
	maxIdempotencyKey = 128
)

type idempotencyRecord struct {
	RequestHash string `json:"request_hash"`
}

// messageRequestHash covers attachment count, not ids: a retried CLI send re-uploads files and
// gets new ids, and its key already fingerprints the file contents.
func messageRequestHash(in SendMessageInput) string {
	h := sha256.New()
	for _, part := range []string{in.ConversationID, in.ReplyToMessageID, strings.TrimSpace(in.BodyMarkdown), strconv.Itoa(len(in.AttachmentIDs))} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// replayMessageTx returns the message an earlier request with the same key created. A key reused
// for a different request is a conflict; an expired record is dropped so the key sends anew.
func replayMessageTx(ctx context.Context, tx *sql.Tx, actorID, key, requestHash string) (*domain.Message, error) {
	var resourceID, responseJSON, createdAt string
	err := tx.QueryRowContext(ctx,
		`SELECT resource_id, response_json, created_at FROM idempotency_records
		 WHERE actor_member_id = ? AND action = ? AND idempotency_key = ?`,
		actorID, actionMessageSend, key,
	).Scan(&resourceID, &responseJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "read idempotency record", err)
	}
	if time.Since(store.ParseTime(createdAt)) > idempotencyWindow {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM idempotency_records WHERE actor_member_id = ? AND action = ? AND idempotency_key = ?`,
			actorID, actionMessageSend, key)
		if err != nil {
			return nil, domain.WrapError(domain.CodeInternal, "expire idempotency record", err)
		}
		return nil, nil
	}
	var rec idempotencyRecord
	_ = json.Unmarshal([]byte(responseJSON), &rec)
	if rec.RequestHash != requestHash {
		return nil, domain.NewError(domain.CodeConflict, "idempotency key was already used for a different message")
	}
	var convID string
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT conversation_id, seq FROM messages WHERE id = ?`, resourceID).Scan(&convID, &seq); err != nil {
		return nil, domain.WrapError(domain.CodeInternal, "load replayed message", err)
	}
	msgs, err := queryMessages(ctx, tx, convID, seq-1, 1, "")
	if err != nil || len(msgs) == 0 {
		return nil, domain.WrapError(domain.CodeInternal, "load replayed message", err)
	}
	return &msgs[0], nil
}

func recordIdempotencyTx(ctx context.Context, tx *sql.Tx, actorID, key, requestHash, messageID string) error {
	raw, _ := json.Marshal(idempotencyRecord{RequestHash: requestHash})
	_, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency_records (actor_member_id, action, idempotency_key, resource_id, response_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		actorID, actionMessageSend, key, messageID, string(raw), store.FormatTime(time.Now().UTC()),
	)
	if err != nil {
		return domain.WrapError(domain.CodeInternal, "record idempotency key", err)
	}
	return nil
}
