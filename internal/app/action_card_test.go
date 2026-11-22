package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

func TestAgentCreateActionCardLifecycle(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	convs, err := svc.ListConversations(ctx, owner)
	if err != nil || len(convs) == 0 {
		t.Fatalf("list conversations: %v", err)
	}
	general := convs[0]

	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Lin",
		RoleText:     "Lead engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Lin: %v", err)
	}

	// Without agent.create permission, an agent cannot even propose an action card.
	var domErr *domain.Error
	if _, err := svc.ProposeAgentCreate(ctx, lin, CreateAgentInput{
		DisplayName:          "Helper",
		RoleText:             "Should be denied",
		ComputerID:           comp.ID,
		SourceConversationID: general.ID,
	}); !errors.As(err, &domErr) || domErr.Code != domain.CodePermissionDenied {
		t.Fatalf("expected permission_denied without agent.create, got %v", err)
	}

	// Grant agent.create -> Lin can propose a pending agent.create action card.
	if err := svc.SetMemberPermission(ctx, owner, lin.ID, domain.PermAgentCreate, true); err != nil {
		t.Fatalf("grant agent.create: %v", err)
	}
	cardMsg, err := svc.ProposeAgentCreate(ctx, lin, CreateAgentInput{
		DisplayName: "Helper",
		RoleText:    "Code reviewer",
		ComputerID:  comp.ID,
		DriverKind:  domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{
			BaseURL: "http://127.0.0.1:4000/v1",
			Model:   "gpt-5-mini",
			APIKey:  "sk-must-not-leak",
		},
		SourceConversationID: general.ID,
	})
	if err != nil {
		t.Fatalf("ProposeAgentCreate: %v", err)
	}
	if cardMsg.Kind != domain.MessageKindAction || cardMsg.ActionKind != domain.ActionActionCard {
		t.Fatalf("unexpected card message kind=%q action_kind=%q", cardMsg.Kind, cardMsg.ActionKind)
	}
	var payload domain.ActionCardPayload
	if err := json.Unmarshal([]byte(cardMsg.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.CardType != domain.ActionCardAgentCreate || payload.Status != domain.ActionCardPending {
		t.Fatalf("unexpected payload header: %+v", payload)
	}
	if string(payload.Params) == "" || json.Valid(payload.Params) == false {
		t.Fatalf("invalid params json: %q", string(payload.Params))
	}
	var params domain.AgentCreateCardParams
	if err := json.Unmarshal(payload.Params, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.DisplayName != "Helper" || params.RoleText != "Code reviewer" || (params.DriverConfig.APIKeySet != nil && *params.DriverConfig.APIKeySet) {
		t.Fatalf("unexpected params: %+v", params)
	}
	if strings.Contains(cardMsg.PayloadJSON, "sk-must-not-leak") {
		t.Fatalf("action card payload must never store secret api_key")
	}

	// Confirm the card as human owner with customized display name and role.
	created, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:         "Helper_Custom",
		RoleText:            "Senior reviewer",
		ComputerID:          comp.ID,
		DriverKind:          domain.DriverBuiltin,
		DriverConfig:        testBuiltinConfig,
		ActionCardMessageID: cardMsg.ID,
	})
	if err != nil {
		t.Fatalf("CreateAgent from card: %v", err)
	}
	if created.DisplayName != "Helper_Custom" {
		t.Fatalf("unexpected created agent: %+v", created)
	}

	// Card must now be marked executed with the created agent result.
	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: general.ID, Limit: 20})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	var updatedPayload domain.ActionCardPayload
	for _, m := range page.Messages {
		if m.ID == cardMsg.ID {
			if err := json.Unmarshal([]byte(m.PayloadJSON), &updatedPayload); err != nil {
				t.Fatalf("unmarshal updated payload: %v", err)
			}
		}
	}
	if updatedPayload.Status != domain.ActionCardExecuted || updatedPayload.ExecutedByMemberID != owner.ID {
		t.Fatalf("expected executed status, got %+v", updatedPayload)
	}
	var res domain.AgentCreateCardResult
	if err := json.Unmarshal(updatedPayload.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.AgentID != created.ID || res.DisplayName != "Helper_Custom" {
		t.Fatalf("unexpected result: %+v", res)
	}

	// Re-confirming the same card must conflict rather than creating a duplicate agent.
	if _, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:         "Helper_Dup",
		RoleText:            "Dup",
		ComputerID:          comp.ID,
		DriverKind:          domain.DriverBuiltin,
		DriverConfig:        testBuiltinConfig,
		ActionCardMessageID: cardMsg.ID,
	}); !errors.As(err, &domErr) || domErr.Code != domain.CodeConflict {
		t.Fatalf("expected conflict on re-executing card, got %v", err)
	}

	// Propose without explicit source_conversation_id -> falls back to active #general, then dismiss.
	card2, err := svc.ProposeAgentCreate(ctx, lin, CreateAgentInput{
		RoleText:   "Docs writer",
		ComputerID: comp.ID,
		DriverKind: domain.DriverBuiltin,
	})
	if err != nil {
		t.Fatalf("ProposeAgentCreate fallback conv: %v", err)
	}
	if card2.ConversationID != general.ID {
		t.Fatalf("expected fallback to #general %s, got %s", general.ID, card2.ConversationID)
	}
	dismissed, err := svc.DismissActionCard(ctx, owner, card2.ID)
	if err != nil {
		t.Fatalf("DismissActionCard: %v", err)
	}
	if dismissed.Status != domain.ActionCardDismissed {
		t.Fatalf("expected dismissed status, got %+v", dismissed)
	}
	if _, err := svc.DismissActionCard(ctx, owner, card2.ID); !errors.As(err, &domErr) || domErr.Code != domain.CodeConflict {
		t.Fatalf("expected conflict dismissing already dismissed card, got %v", err)
	}
}
