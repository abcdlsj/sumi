package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
)

const testSecret = "sk-test-secret-value"

func storedDriverConfig(t *testing.T, svc *Service, agentID string) (string, domain.DriverConfig) {
	t.Helper()
	var kind, raw string
	if err := svc.store.ReadDB().QueryRow(`SELECT driver_kind, driver_config_json FROM agents WHERE member_id = ?`, agentID).Scan(&kind, &raw); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	return kind, domain.ParseDriverConfig(raw)
}

func TestCreateAgentValidatesAndRedactsDriverConfig(t *testing.T) {
	svc, spy, space, owner, comp := setupTestService(t)
	ctx := context.Background()

	_, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Bad", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{BaseURL: "not a url", Model: "m"},
	})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeInvalidArgument || !strings.Contains(de.Message, "driver_config.base_url") {
		t.Fatalf("want invalid base_url error, got %v", err)
	}
	if _, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "BadACP", ComputerID: comp.ID, DriverKind: domain.DriverACP,
	}); !errors.As(err, &de) || !strings.Contains(de.Message, "driver_config.provider") {
		t.Fatalf("want missing provider error, got %v", err)
	}

	agent, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{BaseURL: "https://api.example.com/v1", Model: "gpt", APIKey: testSecret},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	raw, _ := json.Marshal(agent)
	if strings.Contains(string(raw), testSecret) || !strings.Contains(string(raw), `"api_key_set":true`) {
		t.Fatalf("create response must redact api_key: %s", raw)
	}
	members, err := svc.ListMembers(ctx, space.ID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	raw, _ = json.Marshal(members)
	if strings.Contains(string(raw), testSecret) || !strings.Contains(string(raw), `"api_key_set":true`) {
		t.Fatalf("members must redact api_key: %s", raw)
	}

	// The provision spec for the hosting Computer keeps the full config.
	if len(spy.controls) != 1 || spy.controls[0].Spec == nil || spy.controls[0].Spec.DriverConfig.APIKey != testSecret {
		t.Fatalf("provision spec must carry api_key, got %+v", spy.controls)
	}
	specs, err := svc.SetComputerStatus(ctx, comp.ID, "online", &ComputerHello{DaemonVersion: "2.0.0"})
	if err != nil || len(specs) != 1 || specs[0].DriverConfig.APIKey != testSecret {
		t.Fatalf("hello_ack spec must carry api_key, got %+v err=%v", specs, err)
	}
}

func TestUpdateAgentDriverKeepsKeyAndReprovisions(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	agent, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{BaseURL: "https://a.example/v1", Model: "m1", APIKey: testSecret},
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	updated, err := svc.UpdateAgentDriver(ctx, owner, agent.ID, domain.DriverBuiltin,
		domain.DriverConfig{BaseURL: "https://b.example/v1", Model: "m2"})
	if err != nil {
		t.Fatalf("update driver: %v", err)
	}
	if v := updated.Agent.DriverConfig; v.BaseURL != "https://b.example/v1" || v.Model != "m2" || v.APIKeySet == nil || !*v.APIKeySet {
		t.Fatalf("unexpected view %+v", v)
	}
	raw, _ := json.Marshal(updated)
	if strings.Contains(string(raw), testSecret) {
		t.Fatalf("update response leaked api_key: %s", raw)
	}
	if _, cfg := storedDriverConfig(t, svc, agent.ID); cfg.APIKey != testSecret || cfg.Model != "m2" {
		t.Fatalf("omitted api_key must keep stored key, got %+v", cfg)
	}
	last := spy.controls[len(spy.controls)-1]
	if last.Action != "provision" || last.Spec == nil || last.Spec.DriverConfig.Model != "m2" || last.Spec.DriverConfig.APIKey != testSecret {
		t.Fatalf("expected re-provision with new full spec, got %+v", last)
	}

	// A new key replaces the old one.
	if _, err := svc.UpdateAgentDriver(ctx, owner, agent.ID, domain.DriverBuiltin,
		domain.DriverConfig{BaseURL: "https://b.example/v1", Model: "m2", APIKey: "sk-new"}); err != nil {
		t.Fatalf("update key: %v", err)
	}
	if _, cfg := storedDriverConfig(t, svc, agent.ID); cfg.APIKey != "sk-new" {
		t.Fatalf("expected new key stored")
	}

	// Switching kind drops builtin fields entirely.
	updated, err = svc.UpdateAgentDriver(ctx, owner, agent.ID, domain.DriverACP, domain.DriverConfig{Provider: "pi"})
	if err != nil {
		t.Fatalf("switch to acp: %v", err)
	}
	if kind, cfg := storedDriverConfig(t, svc, agent.ID); kind != "acp" || cfg.APIKey != "" || cfg.Provider != "pi" {
		t.Fatalf("switch kind stored %s %+v", kind, cfg)
	}
	if updated.Agent.DriverKind != domain.DriverACP || updated.Agent.DriverConfig.Provider != "pi" {
		t.Fatalf("unexpected acp view %+v", updated.Agent)
	}

	if _, err := svc.UpdateAgentDriver(ctx, owner, agent.ID, domain.DriverACP, domain.DriverConfig{Provider: "nope"}); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestUpdateAgentDriverRequiresPermission(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	_, err = svc.UpdateAgentDriver(ctx, lin, lin.ID, domain.DriverACP, domain.DriverConfig{Provider: "pi"})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodePermissionDenied {
		t.Fatalf("want permission_denied, got %v", err)
	}
}

func TestAgentRuntimeErrorDetailBoundedAndCleared(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := svc.UpdateAgentRuntimeState(ctx, comp.ID, lin.ID, domain.RuntimeError, "", domain.AgentErrModelAuthFailed, strings.Repeat("x", 1000), nil); err != nil {
		t.Fatalf("report error: %v", err)
	}
	m, err := svc.GetMember(ctx, space.ID, lin.ID)
	if err != nil {
		t.Fatalf("get member: %v", err)
	}
	if m.Agent.LastErrorCode != domain.AgentErrModelAuthFailed || len([]rune(m.Agent.LastErrorDetail)) != domain.MaxAgentErrorDetail {
		t.Fatalf("unexpected error fields %q len=%d", m.Agent.LastErrorCode, len(m.Agent.LastErrorDetail))
	}
	// Non-error states clear code and detail even if a stale code is sent.
	if err := svc.UpdateAgentRuntimeState(ctx, comp.ID, lin.ID, domain.RuntimeIdle, "", "stale", "stale", nil); err != nil {
		t.Fatalf("report idle: %v", err)
	}
	m, _ = svc.GetMember(ctx, space.ID, lin.ID)
	if m.Agent.LastErrorCode != "" || m.Agent.LastErrorDetail != "" {
		t.Fatalf("expected cleared error, got %+v", m.Agent)
	}
}

func TestComputerHelloStoresACPProviders(t *testing.T) {
	svc, _, space, _, comp := setupTestService(t)
	ctx := context.Background()
	if _, err := svc.SetComputerStatus(ctx, comp.ID, "online", &ComputerHello{
		DaemonVersion: "2.1.0", ACPProviders: []string{"claude", "bogus", "pi"},
	}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	// Heartbeats must not erase capabilities.
	if _, err := svc.SetComputerStatus(ctx, comp.ID, "online", nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	comps, err := svc.ListComputers(ctx, space.ID)
	if err != nil || len(comps) != 1 {
		t.Fatalf("list computers: %v", err)
	}
	if got := strings.Join(comps[0].ACPProviders, ","); got != "pi,claude" || comps[0].DaemonVersion != "2.1.0" {
		t.Fatalf("acp_providers=%q version=%q", got, comps[0].DaemonVersion)
	}
}

func TestAgentActivityIsEphemeralAndOnlyWhileWorking(t *testing.T) {
	svc, _, space, owner, comp := setupTestService(t)
	ctx := context.Background()
	lin, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName: "Lin", ComputerID: comp.ID, DriverKind: domain.DriverBuiltin, DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateAgentRuntimeState(ctx, comp.ID, lin.ID, domain.RuntimeWorking, "", "", "", &domain.AgentActivity{Tool: "execute", StalledSeconds: 700}); err != nil {
		t.Fatal(err)
	}
	m, _ := svc.GetMember(ctx, space.ID, lin.ID)
	if a := m.Agent.Activity; a == nil || a.Tool != "execute" || a.StalledSeconds != 700 {
		t.Fatalf("working agent must expose activity: %+v", a)
	}
	if err := svc.UpdateAgentRuntimeState(ctx, comp.ID, lin.ID, domain.RuntimeIdle, "", "", "", &domain.AgentActivity{Tool: "execute"}); err != nil {
		t.Fatal(err)
	}
	m, _ = svc.GetMember(ctx, space.ID, lin.ID)
	if m.Agent.Activity != nil {
		t.Fatalf("idle agent must not expose activity: %+v", m.Agent.Activity)
	}
}
