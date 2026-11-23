package app

import (
	"context"
	"fmt"
	"sync"
	"testing"

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
