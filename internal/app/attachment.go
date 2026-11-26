package app

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/store"
)

// Safe extension fallback when uploaders (e.g. Go multipart.CreateFormFile) send application/octet-stream.
// Deliberately excludes active web types (HTML, SVG, JS) to keep attachment serving XSS-safe under nosniff.
var safeAttachmentMediaTypes = map[string]string{
	".png":      "image/png",
	".jpg":      "image/jpeg",
	".jpeg":     "image/jpeg",
	".gif":      "image/gif",
	".webp":     "image/webp",
	".avif":     "image/avif",
	".pdf":      "application/pdf",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".txt":      "text/plain",
	".log":      "text/plain",
	".diff":     "text/plain",
	".patch":    "text/plain",
	".csv":      "text/csv",
	".tsv":      "text/tab-separated-values",
	".json":     "application/json",
	".yaml":     "text/yaml",
	".yml":      "text/yaml",
	".toml":     "text/plain",
}

func inferAttachmentMediaType(name, mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	if mediaType != "" && mediaType != "application/octet-stream" {
		return mediaType
	}
	if inferred, ok := safeAttachmentMediaTypes[strings.ToLower(filepath.Ext(strings.TrimSpace(name)))]; ok {
		return inferred
	}
	return "application/octet-stream"
}

// UploadAttachment streams content into the content-addressed object store and records metadata.
// The attachment is unbound until a message references it via SendMessageInput.AttachmentIDs.
func (s *Service) UploadAttachment(ctx context.Context, actor domain.Member, name, mediaType string, content io.Reader) (domain.Attachment, error) {
	if actor.RetiredAt != nil {
		return domain.Attachment{}, domain.NewError(domain.CodePermissionDenied, "retired member cannot upload attachments")
	}
	if err := domain.ValidateAttachmentName(name); err != nil {
		return domain.Attachment{}, err
	}
	mediaType = inferAttachmentMediaType(name, mediaType)

	sha, size, err := s.store.Objects().Put(content, domain.MaxAttachmentBytes)
	if err != nil {
		if errors.Is(err, store.ErrObjectTooLarge) {
			return domain.Attachment{}, domain.NewError(domain.CodeInvalidArgument, "attachment exceeds 25 MiB limit")
		}
		return domain.Attachment{}, domain.WrapError(domain.CodeInternal, "store attachment object", err)
	}

	now := time.Now().UTC()
	att := domain.Attachment{
		ID:               uuid.NewString(),
		SpaceID:          actor.SpaceID,
		UploaderMemberID: actor.ID,
		Name:             strings.TrimSpace(name),
		MediaType:        mediaType,
		SizeBytes:        size,
		CreatedAt:        now,
	}
	err = s.store.WithWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO attachments (id, space_id, uploader_member_id, name, media_type, size_bytes, sha256, object_path, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			att.ID, att.SpaceID, att.UploaderMemberID, att.Name, att.MediaType, att.SizeBytes, sha, store.ObjectPath(sha), store.FormatTime(now),
		)
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "insert attachment", err)
		}
		return nil
	})
	if err != nil {
		_ = s.store.Objects().Remove(sha)
		return domain.Attachment{}, err
	}
	return att, nil
}

// bindAttachmentsTx links uploaded attachments to a freshly inserted message.
// Only the uploader may attach, and each attachment binds to exactly one message.
func bindAttachmentsTx(ctx context.Context, tx *sql.Tx, actor domain.Member, messageID, spaceID string, attachmentIDs []string) error {
	if len(attachmentIDs) > domain.MaxAttachmentsPerMessage {
		return domain.NewError(domain.CodeInvalidArgument, "at most 10 attachments per message")
	}
	seen := make(map[string]struct{}, len(attachmentIDs))
	position := 0
	for _, id := range attachmentIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			return domain.NewError(domain.CodeInvalidArgument, "duplicate attachment_id")
		}
		seen[id] = struct{}{}

		var space, uploader string
		err := tx.QueryRowContext(ctx,
			`SELECT space_id, uploader_member_id FROM attachments WHERE id = ?`,
			id,
		).Scan(&space, &uploader)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.NewError(domain.CodeNotFound, "attachment not found")
		}
		if err != nil {
			return domain.WrapError(domain.CodeInternal, "query attachment", err)
		}
		if space != spaceID {
			return domain.NewError(domain.CodeInvalidArgument, "attachment belongs to another space")
		}
		if uploader != actor.ID {
			return domain.NewError(domain.CodePermissionDenied, "attachment uploaded by another member")
		}

		_, err = tx.ExecContext(ctx,
			`INSERT INTO message_attachments (message_id, attachment_id, position) VALUES (?, ?, ?)`,
			messageID, id, position,
		)
		if err != nil {
			if store.IsUniqueViolation(err) {
				return domain.NewError(domain.CodeConflict, "attachment already bound to a message")
			}
			return domain.WrapError(domain.CodeInternal, "bind attachment", err)
		}
		position++
	}
	return nil
}

// loadMessageAttachments fills Message.Attachments for the given messages.
func (s *Service) loadMessageAttachments(ctx context.Context, msgs []domain.Message) {
	for i := range msgs {
		rows, err := s.store.ReadDB().QueryContext(ctx,
			`SELECT a.id, a.space_id, a.uploader_member_id, a.name, a.media_type, a.size_bytes, a.created_at
			 FROM message_attachments ma
			 JOIN attachments a ON a.id = ma.attachment_id
			 WHERE ma.message_id = ?
			 ORDER BY ma.position ASC`,
			msgs[i].ID,
		)
		if err != nil {
			continue
		}
		for rows.Next() {
			var att domain.Attachment
			var createdAt string
			if err := rows.Scan(&att.ID, &att.SpaceID, &att.UploaderMemberID, &att.Name, &att.MediaType, &att.SizeBytes, &createdAt); err != nil {
				break
			}
			att.MessageID = msgs[i].ID
			att.MediaType = inferAttachmentMediaType(att.Name, att.MediaType)
			att.CreatedAt = store.ParseTime(createdAt)
			msgs[i].Attachments = append(msgs[i].Attachments, att)
		}
		_ = rows.Close()
	}
}

type AttachmentContent struct {
	Attachment domain.Attachment
	File       *os.File
}

// OpenAttachment authorizes a viewer and returns the stored bytes.
// Unbound attachments are visible only to their uploader.
func (s *Service) OpenAttachment(ctx context.Context, viewer domain.Member, attachmentID string) (AttachmentContent, error) {
	var att domain.Attachment
	var sha, createdAt string
	var messageID sql.NullString
	err := s.store.ReadDB().QueryRowContext(ctx,
		`SELECT a.id, a.space_id, a.uploader_member_id, a.name, a.media_type, a.size_bytes, a.sha256, a.created_at,
		        (SELECT ma.message_id FROM message_attachments ma WHERE ma.attachment_id = a.id)
		 FROM attachments a WHERE a.id = ?`,
		attachmentID,
	).Scan(&att.ID, &att.SpaceID, &att.UploaderMemberID, &att.Name, &att.MediaType, &att.SizeBytes, &sha, &createdAt, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return AttachmentContent{}, domain.NewError(domain.CodeNotFound, "attachment not found")
	}
	if err != nil {
		return AttachmentContent{}, domain.WrapError(domain.CodeInternal, "query attachment", err)
	}
	if att.SpaceID != viewer.SpaceID {
		return AttachmentContent{}, domain.NewError(domain.CodePermissionDenied, "attachment belongs to another space")
	}
	att.MediaType = inferAttachmentMediaType(att.Name, att.MediaType)
	att.CreatedAt = store.ParseTime(createdAt)

	if messageID.Valid {
		att.MessageID = messageID.String
		var convID string
		if err := s.store.ReadDB().QueryRowContext(ctx,
			`SELECT conversation_id FROM messages WHERE id = ?`, att.MessageID,
		).Scan(&convID); err != nil {
			return AttachmentContent{}, domain.WrapError(domain.CodeNotFound, "attachment message not found", err)
		}
		if _, err := canAccessConversation(ctx, s.store.ReadDB(), viewer, convID); err != nil {
			return AttachmentContent{}, err
		}
	} else if att.UploaderMemberID != viewer.ID {
		return AttachmentContent{}, domain.NewError(domain.CodePermissionDenied, "attachment is not visible")
	}

	file, err := s.store.Objects().Open(sha)
	if err != nil {
		return AttachmentContent{}, domain.WrapError(domain.CodeNotFound, "attachment content missing", err)
	}
	return AttachmentContent{Attachment: att, File: file}, nil
}

// AttachmentContentType normalizes an attachment media type for HTTP responses.
func AttachmentContentType(att domain.Attachment) string {
	return inferAttachmentMediaType(att.Name, att.MediaType)
}
