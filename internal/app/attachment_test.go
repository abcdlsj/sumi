package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestAttachmentUploadBindAndVisibility(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	conv, err := svc.CreateChannel(ctx, owner, CreateChannelInput{Slug: "attach", Kind: domain.ConversationPrivate})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	att, err := svc.UploadAttachment(ctx, owner, "notes.txt", "text/plain", strings.NewReader("hello attachment"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if att.SizeBytes != int64(len("hello attachment")) || att.Name != "notes.txt" {
		t.Fatalf("unexpected attachment metadata: %+v", att)
	}

	// Unbound attachments are visible only to their uploader.
	if _, err := svc.OpenAttachment(ctx, owner, att.ID); err != nil {
		t.Fatalf("uploader should read own attachment: %v", err)
	}

	res, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: conv.ID,
		BodyMarkdown:   "with attachment",
		AttachmentIDs:  []string{att.ID},
	})
	if err != nil {
		t.Fatalf("send with attachment: %v", err)
	}
	if len(res.Message.Attachments) != 1 || res.Message.Attachments[0].ID != att.ID {
		t.Fatalf("message should carry the bound attachment: %+v", res.Message.Attachments)
	}

	// Rebinding the same attachment must be rejected.
	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: conv.ID,
		BodyMarkdown:   "reuse",
		AttachmentIDs:  []string{att.ID},
	}); err == nil {
		t.Fatal("rebinding a bound attachment must fail")
	}

	// A member without conversation access must not read the bound attachment.
	other, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Outsider",
		RoleText:    "not a member of #attach",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if _, err := svc.OpenAttachment(ctx, other, att.ID); err == nil {
		t.Fatal("non-member must not read attachment from a private boundary")
	}

	// Cross-space access is always denied.
	if att.SpaceID != space.ID {
		t.Fatalf("attachment space mismatch")
	}
}

func TestAttachmentCapsAndValidation(t *testing.T) {
	svc, _, _, owner, _ := setupTestService(t)
	ctx := context.Background()

	if _, err := svc.UploadAttachment(ctx, owner, "../escape.txt", "text/plain", strings.NewReader("x")); err == nil {
		t.Fatal("path traversal names must be rejected")
	}

	oversized := strings.NewReader(strings.Repeat("a", int(domain.MaxAttachmentBytes)+1))
	if _, err := svc.UploadAttachment(ctx, owner, "big.bin", "application/octet-stream", oversized); err == nil {
		t.Fatal("oversized upload must be rejected")
	}

	att, err := svc.UploadAttachment(ctx, owner, "dup.txt", "text/plain", strings.NewReader("same bytes"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	dup, err := svc.UploadAttachment(ctx, owner, "dup-2.txt", "text/plain", strings.NewReader("same bytes"))
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if att.ID == dup.ID {
		t.Fatal("identical content must still get distinct attachment rows")
	}
	first, err := svc.OpenAttachment(ctx, owner, att.ID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.File.Close()
	body, _ := io.ReadAll(first.File)
	if string(body) != "same bytes" {
		t.Fatalf("deduplicated content mismatch: %q", body)
	}
}

func TestBindAttachmentsRejectsForeignUploader(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	conv, err := svc.CreateChannel(ctx, owner, CreateChannelInput{Slug: "shared", Kind: domain.ConversationPublic})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	agent, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "LinAttach",
		RoleText:    "helper",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	att, err := svc.UploadAttachment(ctx, owner, "secret.txt", "text/plain", strings.NewReader("owner bytes"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := svc.SendMessage(ctx, agent, SendMessageInput{
		ConversationID: conv.ID,
		BodyMarkdown:   "steal",
		AttachmentIDs:  []string{att.ID},
	}); err == nil {
		t.Fatal("agent must not bind another member's attachment")
	}
}
