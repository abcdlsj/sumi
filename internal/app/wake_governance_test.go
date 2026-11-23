package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func TestAgentChainCircuitBreakerOnTaskAndThread(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	cato, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Cato",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Cato: %v", err)
	}
	iris, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Iris",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Iris: %v", err)
	}
	piper, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Piper",
		RoleText:     "QA",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Piper: %v", err)
	}

	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "game-dev",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{cato.ID, iris.ID, piper.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	task, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Loop breaker test",
		AssigneeMemberID: cato.ID,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskConvID := task.ConversationID
	for _, ag := range []domain.Member{iris, piper} {
		if err := svc.SetFollowing(ctx, ag, taskConvID, true); err != nil {
			t.Fatalf("follow task %s: %v", ag.DisplayName, err)
		}
	}

	takeWakes := func() []protocol.WakeFrame {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		out := append([]protocol.WakeFrame(nil), spy.wakes...)
		spy.wakes = nil
		return out
	}
	countPauseNotices := func(convID string) int {
		page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: convID, Limit: 200})
		if err != nil {
			t.Fatalf("list messages: %v", err)
		}
		n := 0
		for _, m := range page.Messages {
			if m.Kind == domain.MessageKindSystem && m.ActionKind == domain.ActionWakePaused {
				n++
			}
		}
		return n
	}

	// Messages 1..(DefaultAgentChainLimit-1) alternate between Cato and Iris: each wakes both other followers.
	senders := []domain.Member{cato, iris}
	for i := 1; i < DefaultAgentChainLimit; i++ {
		sender := senders[(i-1)%len(senders)]
		takeWakes()
		res, err := svc.SendMessage(ctx, sender, SendMessageInput{
			ConversationID: taskConvID,
			BodyMarkdown:   fmt.Sprintf("Agent turn %d", i),
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if res.WokenCount != 2 || len(takeWakes()) != 2 {
			t.Fatalf("message %d before limit must wake 2 followers, got woken=%d", i, res.WokenCount)
		}
		// Mark read for the next sender so reads stay realistic.
		next := senders[i%len(senders)]
		if _, err := svc.ListMessages(ctx, next, ListMessagesInput{ConversationID: taskConvID, UnreadOnly: true, MarkRead: true}); err != nil {
			t.Fatalf("mark read %d: %v", i, err)
		}
	}
	if got := countPauseNotices(taskConvID); got != 0 {
		t.Fatalf("no pause notice expected before limit, got %d", got)
	}

	// Message N (10th consecutive agent text): trips the breaker, suppresses following wakes, emits 1 system notice.
	takeWakes()
	tripSender := senders[(DefaultAgentChainLimit-1)%len(senders)]
	res, err := svc.SendMessage(ctx, tripSender, SendMessageInput{
		ConversationID: taskConvID,
		BodyMarkdown:   fmt.Sprintf("Agent turn %d", DefaultAgentChainLimit),
	})
	if err != nil {
		t.Fatalf("send %d: %v", DefaultAgentChainLimit, err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("tripping message must suppress following wakes, got woken=%d", res.WokenCount)
	}
	if got := countPauseNotices(taskConvID); got != 1 {
		t.Fatalf("expected exactly 1 system pause notice, got %d", got)
	}

	// Triggering agent's read_seq must advance past the system notice so it is not freshness-held by its own trip.
	if _, err := svc.SendMessage(ctx, tripSender, SendMessageInput{
		ConversationID:   taskConvID,
		BodyMarkdown:     "Message 11 while paused",
		EnforceFreshness: true,
	}); err != nil {
		t.Fatalf("send 11 with freshness: %v", err)
	}
	// Message 11 must still wake 0 followers and must NOT emit a duplicate system notice.
	if w := takeWakes(); len(w) != 0 {
		t.Fatalf("message 11 while paused must not wake followers, got %+v", w)
	}
	if got := countPauseNotices(taskConvID); got != 1 {
		t.Fatalf("must not emit duplicate pause notices, got %d", got)
	}

	// Agent inbox (needs_me) suppresses paused following item, while Human owner still sees it.
	catoNeeds, err := svc.ListInbox(ctx, cato, "needs_me")
	if err != nil || len(catoNeeds) != 0 {
		t.Fatalf("paused agent needs_me must be empty, got %+v err=%v", catoNeeds, err)
	}
	ownerNeeds, err := svc.ListInbox(ctx, owner, "needs_me")
	if err != nil || len(ownerNeeds) != 1 || ownerNeeds[0].ConversationID != taskConvID {
		t.Fatalf("human follower must still see paused task in needs_me, got %+v err=%v", ownerNeeds, err)
	}

	// Explicit @mention pierces the pause for the mentioned agent only; unmentioned followers stay paused.
	takeWakes()
	res, err = svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID: taskConvID,
		BodyMarkdown:   "@Iris please inspect this one detail",
	})
	if err != nil {
		t.Fatalf("explicit mention while paused: %v", err)
	}
	wakes := takeWakes()
	if res.WokenCount != 1 || len(wakes) != 1 || wakes[0].AgentMemberID != iris.ID || wakes[0].Reason != "mention" {
		t.Fatalf("explicit mention must wake only Iris with reason=mention, got woken=%d wakes=%+v", res.WokenCount, wakes)
	}
	irisNeeds, err := svc.ListInbox(ctx, iris, "needs_me")
	if err != nil || len(irisNeeds) != 1 || irisNeeds[0].Reason != "mention" {
		t.Fatalf("mentioned agent must see reason=mention in needs_me, got %+v err=%v", irisNeeds, err)
	}
	piperNeeds, err := svc.ListInbox(ctx, piper, "needs_me")
	if err != nil || len(piperNeeds) != 0 {
		t.Fatalf("unmentioned follower must remain suppressed in needs_me, got %+v err=%v", piperNeeds, err)
	}

	// Human message resets the streak and resumes following wakes for all followers.
	takeWakes()
	res, err = svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: taskConvID,
		BodyMarkdown:   "Human check-in: proceed with option B",
	})
	if err != nil {
		t.Fatalf("human resume send: %v", err)
	}
	if res.WokenCount != 3 || len(takeWakes()) != 3 {
		t.Fatalf("human message must resume following wakes for all 3 agents, got woken=%d", res.WokenCount)
	}
	// Agent reply right after human message wakes followers again.
	takeWakes()
	res, err = svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID: taskConvID,
		BodyMarkdown:   "Following up on option B",
	})
	if err != nil {
		t.Fatalf("agent reply after human: %v", err)
	}
	if res.WokenCount != 2 || len(takeWakes()) != 2 {
		t.Fatalf("agent reply after human reset must wake followers again, got woken=%d", res.WokenCount)
	}

	// Thread parity: verify the same circuit breaker works in a channel thread.
	root, err := svc.SendMessage(ctx, owner, SendMessageInput{ConversationID: ch.ID, BodyMarkdown: "Thread root"})
	if err != nil {
		t.Fatalf("thread root: %v", err)
	}
	firstReply, err := svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID:   ch.ID,
		ReplyToMessageID: root.Message.ID,
		BodyMarkdown:     "Thread agent msg 1",
	})
	if err != nil {
		t.Fatalf("thread reply 1: %v", err)
	}
	threadID := firstReply.Message.ConversationID
	if err := svc.SetFollowing(ctx, iris, threadID, true); err != nil {
		t.Fatalf("follow thread: %v", err)
	}
	for i := 2; i < DefaultAgentChainLimit; i++ {
		if _, err := svc.SendMessage(ctx, senders[i%2], SendMessageInput{
			ConversationID: threadID,
			BodyMarkdown:   fmt.Sprintf("Thread agent msg %d", i),
		}); err != nil {
			t.Fatalf("thread msg %d: %v", i, err)
		}
	}
	takeWakes()
	res, err = svc.SendMessage(ctx, iris, SendMessageInput{
		ConversationID: threadID,
		BodyMarkdown:   fmt.Sprintf("Thread agent msg %d", DefaultAgentChainLimit),
	})
	if err != nil {
		t.Fatalf("thread trip msg: %v", err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("thread trip must suppress following wakes, got woken=%d", res.WokenCount)
	}
	if got := countPauseNotices(threadID); got != 1 {
		t.Fatalf("thread must emit 1 system pause notice, got %d", got)
	}
}

func TestAgentChainBreakerNoRedundantNoticeOnTerminalTaskAndConcurrentExactOnce(t *testing.T) {
	svc, _, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	cato, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Cato",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Cato: %v", err)
	}
	iris, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Iris",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Iris: %v", err)
	}
	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "ops",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{cato.ID, iris.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// 1. Terminal task must not emit redundant wake_paused system notices.
	doneTask, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Already done",
		AssigneeMemberID: cato.ID,
	})
	if err != nil {
		t.Fatalf("create done task: %v", err)
	}
	if _, err := svc.UpdateTaskStatus(ctx, cato, doneTask.ID, domain.TaskDone); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	for i := 0; i < DefaultAgentChainLimit+2; i++ {
		if _, err := svc.SendMessage(ctx, cato, SendMessageInput{
			ConversationID: doneTask.ConversationID,
			BodyMarkdown:   fmt.Sprintf("Post-done %d", i),
		}); err != nil {
			t.Fatalf("post-done %d: %v", i, err)
		}
	}
	donePage, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: doneTask.ConversationID, Limit: 200})
	if err != nil {
		t.Fatalf("list done messages: %v", err)
	}
	for _, m := range donePage.Messages {
		if m.Kind == domain.MessageKindSystem && m.ActionKind == domain.ActionWakePaused {
			t.Fatalf("terminal task must not emit wake_paused notice")
		}
	}

	// 2. Concurrent sends crossing DefaultAgentChainLimit emit exactly one system pause notice.
	activeTask, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Concurrent chain",
		AssigneeMemberID: cato.ID,
	})
	if err != nil {
		t.Fatalf("create active task: %v", err)
	}
	if err := svc.SetFollowing(ctx, iris, activeTask.ConversationID, true); err != nil {
		t.Fatalf("follow active task: %v", err)
	}

	totalSends := DefaultAgentChainLimit + 6
	var wg sync.WaitGroup
	errCh := make(chan error, totalSends)
	for i := 0; i < totalSends; i++ {
		sender := cato
		if i%2 == 1 {
			sender = iris
		}
		wg.Add(1)
		go func(idx int, s domain.Member) {
			defer wg.Done()
			_, sendErr := svc.SendMessage(ctx, s, SendMessageInput{
				ConversationID: activeTask.ConversationID,
				BodyMarkdown:   fmt.Sprintf("Concurrent msg %d", idx),
			})
			if sendErr != nil {
				errCh <- sendErr
			}
		}(i, sender)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent send err: %v", err)
	}

	page, err := svc.ListMessages(ctx, owner, ListMessagesInput{ConversationID: activeTask.ConversationID, Limit: 200})
	if err != nil {
		t.Fatalf("list concurrent messages: %v", err)
	}
	notices := 0
	for _, m := range page.Messages {
		if m.Kind == domain.MessageKindSystem && m.ActionKind == domain.ActionWakePaused {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("expected exact-once wake_paused system notice under concurrency, got %d", notices)
	}
}

func countPauseNoticesByTier(t *testing.T, svc *Service, owner domain.Member, convID, tier string) int {
	t.Helper()
	page, err := svc.ListMessages(context.Background(), owner, ListMessagesInput{ConversationID: convID, Limit: 200})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	n := 0
	for _, m := range page.Messages {
		if m.Kind == domain.MessageKindSystem && m.ActionKind == domain.ActionWakePaused {
			if tier == "" {
				n++
				continue
			}
			var p struct {
				Tier string `json:"tier"`
			}
			if json.Unmarshal([]byte(m.PayloadJSON), &p) == nil && p.Tier == tier {
				n++
			}
		}
	}
	return n
}

func TestChannelAllChainPauseAndQuietChannelNoNoise(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	cato, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Cato",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Cato: %v", err)
	}
	iris, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Iris",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Iris: %v", err)
	}

	// 1. Quiet channel (default notify_level=mentions): unmentioned agent messages emit 0 notices.
	quietCh, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "quiet",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{cato.ID, iris.ID},
	})
	if err != nil {
		t.Fatalf("create quiet channel: %v", err)
	}
	for i := 1; i <= DefaultAgentChainLimit+5; i++ {
		sender := cato
		if i%2 == 0 {
			sender = iris
		}
		res, err := svc.SendMessage(ctx, sender, SendMessageInput{
			ConversationID: quietCh.ID,
			BodyMarkdown:   fmt.Sprintf("Quiet note %d", i),
		})
		if err != nil {
			t.Fatalf("quiet msg %d: %v", i, err)
		}
		if res.WokenCount != 0 {
			t.Fatalf("quiet channel must wake 0, got %d", res.WokenCount)
		}
	}
	if got := countPauseNoticesByTier(t, svc, owner, quietCh.ID, ""); got != 0 {
		t.Fatalf("quiet channel must not emit wake_paused notices, got %d", got)
	}

	// 2. Active notify_level=all channel: pauses at DefaultAgentChainLimit, suppresses inbox, resets on human reply.
	loudCh, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "loud",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{cato.ID, iris.ID},
	})
	if err != nil {
		t.Fatalf("create loud channel: %v", err)
	}
	if err := svc.SetNotifyLevel(ctx, iris, loudCh.ID, domain.NotifyAll); err != nil {
		t.Fatalf("set notify all: %v", err)
	}
	if err := svc.SetNotifyLevel(ctx, cato, loudCh.ID, domain.NotifyAll); err != nil {
		t.Fatalf("set notify all cato: %v", err)
	}

	takeWakes := func() []protocol.WakeFrame {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		out := append([]protocol.WakeFrame(nil), spy.wakes...)
		spy.wakes = nil
		return out
	}

	for i := 1; i < DefaultAgentChainLimit; i++ {
		sender := cato
		if i%2 == 0 {
			sender = iris
		}
		takeWakes()
		res, err := svc.SendMessage(ctx, sender, SendMessageInput{
			ConversationID: loudCh.ID,
			BodyMarkdown:   fmt.Sprintf("Channel msg %d", i),
		})
		if err != nil {
			t.Fatalf("loud msg %d: %v", i, err)
		}
		wakes := takeWakes()
		if res.WokenCount != 1 || len(wakes) != 1 || wakes[0].Reason != "channel_all" {
			t.Fatalf("loud msg %d must wake 1 via channel_all, got %d %+v", i, res.WokenCount, wakes)
		}
	}

	// 10th message pauses channel_all wakes and emits 1 passive notice.
	takeWakes()
	res, err := svc.SendMessage(ctx, iris, SendMessageInput{
		ConversationID: loudCh.ID,
		BodyMarkdown:   "Channel msg 10",
	})
	if err != nil {
		t.Fatalf("loud msg 10: %v", err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("10th channel_all msg must be suppressed, got %d", res.WokenCount)
	}
	if got := countPauseNoticesByTier(t, svc, owner, loudCh.ID, wakePauseTierPassive); got != 1 {
		t.Fatalf("expected 1 passive pause notice, got %d", got)
	}

	// Agent needs_me inbox suppresses paused channel_all entry.
	catoNeeds, err := svc.ListInbox(ctx, cato, "needs_me")
	if err != nil || len(catoNeeds) != 0 {
		t.Fatalf("paused channel_all must be suppressed in agent needs_me, got %+v err=%v", catoNeeds, err)
	}

	// Human reply resumes channel_all wakes.
	takeWakes()
	res, err = svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: loudCh.ID,
		BodyMarkdown:   "Human check-in",
	})
	if err != nil {
		t.Fatalf("human reply: %v", err)
	}
	if res.WokenCount != 2 || len(takeWakes()) != 2 {
		t.Fatalf("human reply must wake both agents, got %d", res.WokenCount)
	}
}

func TestAgentHardChainLimitStopsMentionPingPongAndPreservesHumanMention(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	cato, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Cato",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Cato: %v", err)
	}
	iris, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Iris",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Iris: %v", err)
	}
	piper, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Piper",
		RoleText:     "QA",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Piper: %v", err)
	}
	ch, err := svc.CreateChannel(ctx, owner, CreateChannelInput{
		Slug:      "pingpong",
		Kind:      domain.ConversationPrivate,
		MemberIDs: []string{cato.ID, iris.ID, piper.ID},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	// Human mentions Piper early; Piper does NOT read it yet.
	if _, err := svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "@Piper please check when you have a moment",
	}); err != nil {
		t.Fatalf("human mention piper: %v", err)
	}

	takeWakes := func() []protocol.WakeFrame {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		out := append([]protocol.WakeFrame(nil), spy.wakes...)
		spy.wakes = nil
		return out
	}

	// Cato and Iris ping-pong explicit @mentions 1..(DefaultAgentHardChainLimit-1).
	for i := 1; i < DefaultAgentHardChainLimit; i++ {
		sender, targetName := cato, "Iris"
		if i%2 == 0 {
			sender, targetName = iris, "Cato"
		}
		takeWakes()
		res, err := svc.SendMessage(ctx, sender, SendMessageInput{
			ConversationID: ch.ID,
			BodyMarkdown:   fmt.Sprintf("@%s ping %d", targetName, i),
		})
		if err != nil {
			t.Fatalf("mention %d: %v", i, err)
		}
		wakes := takeWakes()
		if res.WokenCount != 1 || len(wakes) != 1 || wakes[0].Reason != "mention" {
			t.Fatalf("mention %d below hard limit must wake 1, got %d %+v", i, res.WokenCount, wakes)
		}
	}
	// Mark Iris read right before the 20th message so any unread mention for Iris is strictly agent-authored.
	if err := svc.MarkRead(ctx, iris, ch.ID, 0); err != nil {
		t.Fatalf("mark iris read: %v", err)
	}

	// 20th consecutive agent message with @Iris hits Tier 2 hard pause: suppresses mention wake, emits 1 tier=all notice.
	takeWakes()
	res, err := svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "@Iris ping 20",
	})
	if err != nil {
		t.Fatalf("mention 20: %v", err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("20th consecutive agent mention must be hard-paused, got %d", res.WokenCount)
	}
	if got := countPauseNoticesByTier(t, svc, owner, ch.ID, wakePauseTierAll); got != 1 {
		t.Fatalf("expected 1 tier=all pause notice, got %d", got)
	}

	// 21st with @all is also suppressed and must not duplicate the tier=all notice.
	takeWakes()
	res, err = svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "@all ping 21",
	})
	if err != nil {
		t.Fatalf("mention 21: %v", err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("21st @all must remain hard-paused, got %d", res.WokenCount)
	}
	if got := countPauseNoticesByTier(t, svc, owner, ch.ID, wakePauseTierAll); got != 1 {
		t.Fatalf("must not duplicate tier=all pause notice, got %d", got)
	}

	// Iris's unread mention is agent-authored -> suppressed from needs_me while hard-paused.
	irisNeeds, err := svc.ListInbox(ctx, iris, "needs_me")
	if err != nil || len(irisNeeds) != 0 {
		t.Fatalf("agent-authored mention while hard-paused must be suppressed in needs_me, got %+v err=%v", irisNeeds, err)
	}
	// Piper's unread mention was authored by the HUMAN owner -> MUST NOT be suppressed!
	piperNeeds, err := svc.ListInbox(ctx, piper, "needs_me")
	if err != nil || len(piperNeeds) != 1 || piperNeeds[0].Reason != "mention" {
		t.Fatalf("unread human mention must remain visible in needs_me even when hard-paused, got %+v err=%v", piperNeeds, err)
	}

	// Human message resets the streak so explicit @mentions work again.
	takeWakes()
	res, err = svc.SendMessage(ctx, owner, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "@Iris please wrap up",
	})
	if err != nil {
		t.Fatalf("human reset: %v", err)
	}
	if res.WokenCount != 1 || len(takeWakes()) != 1 {
		t.Fatalf("human mention must wake immediately and reset streak, got %d", res.WokenCount)
	}
	takeWakes()
	res, err = svc.SendMessage(ctx, iris, SendMessageInput{
		ConversationID: ch.ID,
		BodyMarkdown:   "@Cato synced after human check-in",
	})
	if err != nil {
		t.Fatalf("agent mention after human reset: %v", err)
	}
	if res.WokenCount != 1 || len(takeWakes()) != 1 {
		t.Fatalf("agent mention after human reset must wake again, got %d", res.WokenCount)
	}

	// Terminal task: explicit @mention ping-pong still trips Tier 2 at DefaultAgentHardChainLimit.
	doneTask, err := svc.CreateTask(ctx, owner, CreateTaskInput{
		ChannelID:        ch.ID,
		Title:            "Done mention loop",
		AssigneeMemberID: cato.ID,
	})
	if err != nil {
		t.Fatalf("create done task: %v", err)
	}
	if _, err := svc.UpdateTaskStatus(ctx, cato, doneTask.ID, domain.TaskDone); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	for i := 1; i < DefaultAgentHardChainLimit; i++ {
		sender, targetName := cato, "Iris"
		if i%2 == 0 {
			sender, targetName = iris, "Cato"
		}
		takeWakes()
		res, err := svc.SendMessage(ctx, sender, SendMessageInput{
			ConversationID: doneTask.ConversationID,
			BodyMarkdown:   fmt.Sprintf("@%s post-done %d", targetName, i),
		})
		if err != nil {
			t.Fatalf("done mention %d: %v", i, err)
		}
		if res.WokenCount != 1 {
			t.Fatalf("done mention %d before hard limit must wake 1, got %d", i, res.WokenCount)
		}
	}
	takeWakes()
	res, err = svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID: doneTask.ConversationID,
		BodyMarkdown:   "@Iris post-done 20",
	})
	if err != nil {
		t.Fatalf("done mention 20: %v", err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("done task 20th @mention must be hard-paused, got %d", res.WokenCount)
	}
	if got := countPauseNoticesByTier(t, svc, owner, doneTask.ConversationID, wakePauseTierAll); got != 1 {
		t.Fatalf("done task must emit 1 tier=all pause notice at 20, got %d", got)
	}
}

func TestAgentToAgentDMSlidingWindowAndHumanDMUnaffected(t *testing.T) {
	svc, spy, _, owner, comp := setupTestService(t)
	ctx := context.Background()

	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	svc.nowFn = func() time.Time { return now }

	cato, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Cato",
		RoleText:     "Engineer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Cato: %v", err)
	}
	iris, err := svc.CreateAgent(ctx, owner, CreateAgentInput{
		DisplayName:  "Iris",
		RoleText:     "Reviewer",
		ComputerID:   comp.ID,
		DriverKind:   domain.DriverBuiltin,
		DriverConfig: testBuiltinConfig,
	})
	if err != nil {
		t.Fatalf("create Iris: %v", err)
	}

	takeWakes := func() []protocol.WakeFrame {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		out := append([]protocol.WakeFrame(nil), spy.wakes...)
		spy.wakes = nil
		return out
	}

	// 1. Agent-to-Agent DM throttles at DefaultAgentDMWindowLimit within DefaultAgentDMWindow and auto-recovers.
	a2aDM, err := svc.GetOrCreateDM(ctx, cato, iris.ID)
	if err != nil {
		t.Fatalf("create a2a dm: %v", err)
	}
	for i := 1; i < DefaultAgentDMWindowLimit; i++ {
		sender := cato
		if i%2 == 0 {
			sender = iris
		}
		now = now.Add(10 * time.Second)
		takeWakes()
		res, err := svc.SendMessage(ctx, sender, SendMessageInput{
			ConversationID: a2aDM.ID,
			BodyMarkdown:   fmt.Sprintf("DM turn %d", i),
		})
		if err != nil {
			t.Fatalf("a2a dm %d: %v", i, err)
		}
		wakes := takeWakes()
		if res.WokenCount != 1 || len(wakes) != 1 || wakes[0].Reason != "dm" {
			t.Fatalf("a2a dm %d before limit must wake 1, got %d %+v", i, res.WokenCount, wakes)
		}
	}

	// 10th message within window trips DM rate limit.
	now = now.Add(10 * time.Second)
	takeWakes()
	res, err := svc.SendMessage(ctx, iris, SendMessageInput{
		ConversationID: a2aDM.ID,
		BodyMarkdown:   "DM turn 10",
	})
	if err != nil {
		t.Fatalf("a2a dm 10: %v", err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("10th a2a dm must suppress wake, got %d", res.WokenCount)
	}
	if got := countPauseNoticesByTier(t, svc, iris, a2aDM.ID, wakePauseTierDMRateLimit); got != 1 {
		t.Fatalf("expected 1 dm_rate_limit notice, got %d", got)
	}

	// Triggering agent's read_seq advances past the notice (no self-hold on EnforceFreshness).
	now = now.Add(10 * time.Second)
	takeWakes()
	res, err = svc.SendMessage(ctx, iris, SendMessageInput{
		ConversationID:   a2aDM.ID,
		BodyMarkdown:     "DM turn 11",
		EnforceFreshness: true,
	})
	if err != nil || res.Held {
		t.Fatalf("a2a dm 11 must not self-hold, held=%v err=%v", res.Held, err)
	}
	if res.WokenCount != 0 || len(takeWakes()) != 0 {
		t.Fatalf("11th a2a dm within window must stay suppressed, got %d", res.WokenCount)
	}
	if got := countPauseNoticesByTier(t, svc, iris, a2aDM.ID, wakePauseTierDMRateLimit); got != 1 {
		t.Fatalf("must not duplicate dm_rate_limit notice in same window, got %d", got)
	}

	// Peer's needs_me inbox suppresses the rate-limited agent-to-agent DM.
	catoNeeds, err := svc.ListInbox(ctx, cato, "needs_me")
	if err != nil || len(catoNeeds) != 0 {
		t.Fatalf("rate-limited a2a dm must be suppressed in peer needs_me, got %+v err=%v", catoNeeds, err)
	}

	// Advance clock past the sliding window -> DM wakes automatically recover.
	now = now.Add(DefaultAgentDMWindow + time.Second)
	takeWakes()
	res, err = svc.SendMessage(ctx, cato, SendMessageInput{
		ConversationID: a2aDM.ID,
		BodyMarkdown:   "DM after cooldown",
	})
	if err != nil {
		t.Fatalf("a2a dm after cooldown: %v", err)
	}
	wakes := takeWakes()
	if res.WokenCount != 1 || len(wakes) != 1 || wakes[0].Reason != "dm" {
		t.Fatalf("a2a dm after cooldown must wake peer, got %d %+v", res.WokenCount, wakes)
	}

	// 2. Human-Agent DM is never rate-limited.
	humanDM, err := svc.GetOrCreateDM(ctx, owner, cato.ID)
	if err != nil {
		t.Fatalf("create human dm: %v", err)
	}
	for i := 1; i <= DefaultAgentDMWindowLimit+5; i++ {
		now = now.Add(time.Second)
		takeWakes()
		res, err := svc.SendMessage(ctx, owner, SendMessageInput{
			ConversationID: humanDM.ID,
			BodyMarkdown:   fmt.Sprintf("Human DM %d", i),
		})
		if err != nil {
			t.Fatalf("human dm %d: %v", i, err)
		}
		if res.WokenCount != 1 || len(takeWakes()) != 1 {
			t.Fatalf("human DM %d must always wake agent, got %d", i, res.WokenCount)
		}
	}
	if got := countPauseNoticesByTier(t, svc, owner, humanDM.ID, ""); got != 0 {
		t.Fatalf("human DM must never emit pause notices, got %d", got)
	}
}
