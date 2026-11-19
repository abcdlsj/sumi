package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

// TestWerewolfCommunication verifies autonomous multi-agent collaboration without test-driven phase stepping:
//   - 1 God agent + 4 player agents in a shared channel plus private night DMs
//   - DM visibility isolation (players cannot read other players' private DMs)
//   - Concurrent day-phase speaking with Freshness Guard hold & retry
//   - Clean completion signaled solely by God posting GAME_OVER
func TestWerewolfCommunication(t *testing.T) {
	var binPath string
	var dmIsolationVerified atomic.Bool

	type godState struct {
		mu           sync.Mutex
		phase        string // "" -> "night" -> "day" -> "done"
		nightReplies map[string]bool
		dayReplies   map[string]bool
		wolfDMID     string
	}
	gs := &godState{
		nightReplies: make(map[string]bool),
		dayReplies:   make(map[string]bool),
	}

	var playerSpoke sync.Map // agentName:convID -> bool

	sendWithFreshnessRetry := func(ctx context.Context, in driver.TurnInput, convID, body string) error {
		for attempt := 0; attempt < 6; attempt++ {
			res, code, err := runSumiCLI(ctx, binPath, in, "send", "--body", body, convID)
			if err != nil {
				return err
			}
			if res.OK && code == 0 {
				return nil
			}
			// On Freshness Guard hold, server already advanced read_seq; retry cleanly.
			time.Sleep(15 * time.Millisecond)
		}
		return fmt.Errorf("failed to send after freshness retries")
	}

	factory := func(spec protocol.AssignedAgentSpec, _ string) (driver.SessionDriver, error) {
		return &funcDriver{
			fn: func(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
				inboxRes, _, err := runSumiCLI(ctx, binPath, in, "inbox")
				if err != nil || !inboxRes.OK {
					return driver.TurnResult{}, fmt.Errorf("%s inbox failed: %v", spec.DisplayName, err)
				}
				var inboxPayload struct {
					Items []domain.InboxItem `json:"items"`
				}
				_ = json.Unmarshal(inboxRes.Data, &inboxPayload)

				// Read all unread inbox conversations first.
				type unreadBatch struct {
					item domain.InboxItem
					msgs []domain.Message
				}
				batches := make([]unreadBatch, 0, len(inboxPayload.Items))
				for _, it := range inboxPayload.Items {
					readRes, _, err := runSumiCLI(ctx, binPath, in, "read", "--unread", it.ConversationID)
					if err != nil || !readRes.OK {
						continue
					}
					var mp struct {
						Messages []domain.Message `json:"messages"`
					}
					_ = json.Unmarshal(readRes.Data, &mp)
					batches = append(batches, unreadBatch{item: it, msgs: mp.Messages})
				}

				if spec.DisplayName == "God" {
					membersRes, _, err := runSumiCLI(ctx, binPath, in, "members")
					if err != nil || !membersRes.OK {
						return driver.TurnResult{}, fmt.Errorf("god members failed")
					}
					var memPayload struct {
						Members []domain.Member `json:"members"`
					}
					_ = json.Unmarshal(membersRes.Data, &memPayload)
					byName := make(map[string]domain.Member)
					for _, m := range memPayload.Members {
						byName[m.DisplayName] = m
					}

					convsRes, _, _ := runSumiCLI(ctx, binPath, in, "conversations")
					var convPayload struct {
						Conversations []domain.Conversation `json:"conversations"`
					}
					_ = json.Unmarshal(convsRes.Data, &convPayload)
					var groupID string
					for _, c := range convPayload.Conversations {
						if c.Slug == "werewolf" {
							groupID = c.ID
						}
					}

					gs.mu.Lock()
					defer gs.mu.Unlock()

					if gs.phase == "" {
						// Start Night 1: DM Wolf and Seer secretly.
						gs.phase = "night"
						for _, target := range []struct {
							name string
							msg  string
						}{
							{"Wolf", "NIGHT_ROLE: You are the Werewolf. Reply with your target."},
							{"Seer", "NIGHT_ROLE: You are the Seer. Reply who you inspect."},
						} {
							dmRes, _, err := runSumiCLI(ctx, binPath, in, "dm", byName[target.name].ID)
							if err != nil || !dmRes.OK {
								return driver.TurnResult{}, fmt.Errorf("create dm with %s failed", target.name)
							}
							var dmPayload struct {
								Conversation domain.Conversation `json:"conversation"`
							}
							_ = json.Unmarshal(dmRes.Data, &dmPayload)
							if target.name == "Wolf" {
								gs.wolfDMID = dmPayload.Conversation.ID
							}
							if err := sendWithFreshnessRetry(ctx, in, dmPayload.Conversation.ID, target.msg); err != nil {
								return driver.TurnResult{}, err
							}
						}
						return driver.TurnResult{FinalText: "night started"}, nil
					}

					if gs.phase == "night" {
						for _, b := range batches {
							if b.item.Kind == domain.ConversationDM {
								for _, m := range b.msgs {
									if m.AuthorName == "Wolf" || m.AuthorName == "Seer" {
										gs.nightReplies[m.AuthorName] = true
									}
								}
							}
						}
						if gs.nightReplies["Wolf"] && gs.nightReplies["Seer"] {
							gs.phase = "day"
							if err := sendWithFreshnessRetry(ctx, in, groupID, "@all DAY_PHASE: Dawn breaks. Everyone state your suspicion now."); err != nil {
								return driver.TurnResult{}, err
							}
						}
						return driver.TurnResult{FinalText: "night processed"}, nil
					}

					if gs.phase == "day" {
						for _, b := range batches {
							if b.item.ConversationID == groupID {
								for _, m := range b.msgs {
									if m.AuthorName != "God" && m.AuthorName != "Owner" {
										gs.dayReplies[m.AuthorName] = true
									}
								}
							}
						}
						if len(gs.dayReplies) >= 4 {
							gs.phase = "done"
							if err := sendWithFreshnessRetry(ctx, in, groupID, "GAME_OVER: Villagers win. All players spoke."); err != nil {
								return driver.TurnResult{}, err
							}
						}
						return driver.TurnResult{FinalText: "day processed"}, nil
					}
					return driver.TurnResult{FinalText: "done"}, nil
				}

				// Player behavior: respond to secret night DMs and day channel discussion.
				for _, b := range batches {
					key := spec.DisplayName + ":" + b.item.ConversationID
					if _, already := playerSpoke.Load(key); already {
						continue
					}
					if b.item.Kind == domain.ConversationDM {
						playerSpoke.Store(key, true)
						if err := sendWithFreshnessRetry(ctx, in, b.item.ConversationID, "NIGHT_ACTION from "+spec.DisplayName); err != nil {
							return driver.TurnResult{}, err
						}
						continue
					}
					// In public day phase, VillagerA also probes Wolf's private DM to assert isolation.
					if spec.DisplayName == "VillagerA" {
						gs.mu.Lock()
						wolfDM := gs.wolfDMID
						gs.mu.Unlock()
						if wolfDM != "" {
							probeRes, code, err := runSumiCLI(ctx, binPath, in, "read", wolfDM)
							if err == nil && !probeRes.OK && code != 0 {
								dmIsolationVerified.Store(true)
							}
						}
					}
					for _, m := range b.msgs {
						if strings.Contains(m.BodyMarkdown, "DAY_PHASE") {
							playerSpoke.Store(key, true)
							if err := sendWithFreshnessRetry(ctx, in, b.item.ConversationID, "@God "+spec.DisplayName+" votes based on day discussion."); err != nil {
								return driver.TurnResult{}, err
							}
							break
						}
					}
				}
				return driver.TurnResult{FinalText: "player turn complete"}, nil
			},
		}, nil
	}

	env := startScenarioEnv(t, factory)
	binPath = env.binPath
	ctx := context.Background()

	names := []string{"God", "Wolf", "Seer", "VillagerA", "VillagerB"}
	agents := make([]domain.Member, 0, len(names))
	memberIDs := make([]string, 0, len(names))
	for _, name := range names {
		ag, err := env.svc.CreateAgent(ctx, env.owner, app.CreateAgentInput{
			DisplayName: name,
			RoleText:    "Werewolf participant " + name,
			ComputerID:  env.comp.ID,
			DriverKind:  domain.DriverBuiltin, DriverConfig: scenarioBuiltinConfig,
		})
		if err != nil {
			t.Fatalf("create agent %s: %v", name, err)
		}
		agents = append(agents, ag)
		memberIDs = append(memberIDs, ag.ID)
	}

	group, err := env.svc.CreateChannel(ctx, env.owner, app.CreateChannelInput{
		Slug:      "werewolf",
		Kind:      domain.ConversationPublic,
		MemberIDs: memberIDs,
	})
	if err != nil {
		t.Fatalf("create werewolf channel: %v", err)
	}

	// Kick off the game; test only observes persisted collaboration facts from here.
	if _, err := env.svc.SendMessage(ctx, env.owner, app.SendMessageInput{
		ConversationID: group.ID,
		BodyMarkdown:   "@God start the werewolf game now.",
	}); err != nil {
		t.Fatalf("kickoff werewolf: %v", err)
	}

	waitUntil(t, 20*time.Second, "God posts GAME_OVER", func() bool {
		msgs, _, err := env.svc.ListMessages(ctx, env.owner, app.ListMessagesInput{
			ConversationID: group.ID,
			Limit:          100,
		})
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.AuthorName == "God" && strings.Contains(m.BodyMarkdown, "GAME_OVER") {
				return true
			}
		}
		return false
	})

	if !dmIsolationVerified.Load() {
		t.Fatalf("expected VillagerA probe of Wolf private DM to be denied")
	}

	// Assert all 5 agents participated in the public channel.
	msgs, _, err := env.svc.ListMessages(ctx, env.owner, app.ListMessagesInput{
		ConversationID: group.ID,
		Limit:          100,
	})
	if err != nil {
		t.Fatalf("read final group messages: %v", err)
	}
	speakers := make(map[string]bool)
	for _, m := range msgs {
		speakers[m.AuthorName] = true
	}
	for _, name := range names {
		if !speakers[name] {
			t.Fatalf("expected %s to speak in #werewolf, speakers=%v", name, speakers)
		}
	}
}
