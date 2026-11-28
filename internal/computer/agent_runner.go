package computer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/abcdlsj/sumi/internal/agenthost"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func (d *Daemon) provisionAndWake(ctx context.Context, spec protocol.AssignedAgentSpec, reason string) {
	ag := d.provisionAgent(spec)
	ag.mu.Lock()
	failCode, failDetail, ok, busy, focusConvID := ag.failCode, ag.failDetail, ag.drv != nil, ag.busy, ag.focusConvID
	attachedTurnID := ag.attachedTurnID
	if attachedTurnID != "" {
		ag.attachedTurnID = ""
		ag.busy = true
		ag.dirty = true
		busy = true
	} else if ok && busy {
		// Preserve in-flight turn state across WS reconnects; mark dirty so unread items
		// accumulated during the disconnect are fetched right after the current turn ends.
		ag.dirty = true
	}
	ag.mu.Unlock()
	if !ok {
		d.logger.Warn("Agent provision failed", "agent_id", spec.MemberID, "error_code", failCode)
		d.reportAgentState(spec.MemberID, domain.RuntimeError, "", failCode, failDetail)
		return
	}
	if attachedTurnID != "" {
		d.reportAgentState(spec.MemberID, domain.RuntimeWorking, focusConvID, "", "")
		go d.resumeAttachedTurn(ctx, ag, attachedTurnID, focusConvID)
		return
	}
	if busy {
		d.reportAgentState(spec.MemberID, domain.RuntimeWorking, focusConvID, "", "")
		return
	}
	d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
	d.scheduleWake(ctx, spec.MemberID, "", reason)
}

// loadOrCreateDriverToken persists the per-agent capability token on disk (0600) so an
// agent-host sidecar and its child processes stay authenticated across Computer restarts.
func loadOrCreateDriverToken(homeDir string) string {
	tokenPath := filepath.Join(homeDir, "driver.token")
	if raw, err := os.ReadFile(tokenPath); err == nil {
		if tok := strings.TrimSpace(string(raw)); len(tok) >= 16 {
			return tok
		}
	}
	tokBuf := make([]byte, 24)
	_, _ = rand.Read(tokBuf)
	tok := hex.EncodeToString(tokBuf)
	if err := os.MkdirAll(homeDir, 0o700); err == nil {
		tmp := tokenPath + ".tmp"
		if os.WriteFile(tmp, []byte(tok+"\n"), 0o600) == nil {
			_ = os.Rename(tmp, tokenPath)
		}
	}
	return tok
}

// provisionAgent registers the agent or, when its spec changed, closes the old driver and
// rebuilds from the new spec. Workspace and MEMORY.md persist; the Runtime Session is reset.
func (d *Daemon) provisionAgent(spec protocol.AssignedAgentSpec) *hostedAgent {
	d.mu.Lock()
	ag := d.agents[spec.MemberID]
	if ag == nil {
		homeDir := filepath.Join(d.cfg.DataDir, "agents", spec.MemberID)
		ag = &hostedAgent{
			homeDir:     homeDir,
			workDir:     filepath.Join(homeDir, "workspace"),
			memoryDir:   filepath.Join(homeDir, "memory"),
			driverToken: loadOrCreateDriverToken(homeDir),
		}
		d.agents[spec.MemberID] = ag
		d.tokenToAgent[ag.driverToken] = spec.MemberID
	}
	d.mu.Unlock()

	ag.mu.Lock()
	defer ag.mu.Unlock()
	sameSpec := reflect.DeepEqual(ag.spec, spec)
	if ag.drv != nil && sameSpec {
		if ag.busy || driverHealthy(ag.drv) {
			return ag
		}
	}
	specChanged := ag.drv != nil && !sameSpec
	old := ag.drv
	drv, code, detail := d.buildAgentDriver(ag, spec)
	if drv != nil && specChanged {
		if err := drv.ResetSession(); err != nil {
			d.logger.Warn("Reset session on re-provision failed", "agent_id", spec.MemberID, "error", err.Error())
		}
	}
	ag.spec, ag.drv, ag.failCode, ag.failDetail = spec, drv, code, detail
	ag.stopRequested = false
	if specChanged {
		ag.pendingRetry = nil
	}
	ag.resetBreakerLocked()
	if old != nil {
		oldHC, oldSidecar := old.(*agenthost.Client)
		_, newSidecar := drv.(*agenthost.Client)
		if oldSidecar && newSidecar {
			// AttachOrSpawn already replaced the sidecar behind host.sock; calling old.Close()
			// would send /shutdown to the replacement sidecar listening on the same path.
			oldHC.Detach()
		} else {
			// Closing may wait for an in-flight turn; never block provisioning on it.
			go func() { _ = old.Close() }()
		}
	}
	return ag
}

// ensureAgentHome makes an Agent Home's storage usable again and reports whether it had to be
// rebuilt. It only runs at provisioning otherwise, so an externally removed workspace would
// fail every turn until the spec changes or the Computer restarts.
func ensureAgentHome(ag *hostedAgent) (bool, error) {
	rebuilt := false
	for _, dir := range []string{ag.workDir, ag.memoryDir} {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			rebuilt = true
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return rebuilt, err
		}
	}
	memFile := filepath.Join(ag.memoryDir, "MEMORY.md")
	if _, err := os.Stat(memFile); errors.Is(err, fs.ErrNotExist) {
		rebuilt = true
		_ = os.WriteFile(memFile, []byte(memoryTemplate), 0o600)
	}
	return rebuilt, nil
}

func (d *Daemon) buildAgentDriver(ag *hostedAgent, spec protocol.AssignedAgentSpec) (driver.SessionDriver, string, string) {
	if _, err := ensureAgentHome(ag); err != nil {
		return nil, domain.AgentErrDriverStartFailed, domain.BoundErrorDetail(err.Error())
	}
	if spec.DriverKind == domain.DriverACP && spec.DriverConfig.Provider != "" {
		if err := prepareACPRuntimeDir(ag.homeDir, spec.DriverConfig); err != nil {
			return nil, domain.AgentErrDriverStartFailed, domain.BoundErrorDetail(err.Error())
		}
	}
	var drv driver.SessionDriver
	var err error
	ag.staleBuild = false
	if d.cfg.DriverFactory != nil {
		drv, err = d.cfg.DriverFactory(spec, ag.homeDir)
	} else if d.cfg.UseSidecar {
		attachCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		hc, st, attachErr := agenthost.AttachOrSpawn(attachCtx, agenthost.AttachOptions{
			BinaryPath:    d.cfg.SumiBinary,
			AgentHome:     ag.homeDir,
			Spec:          spec,
			ScrubEnvToken: d.cfg.ComputerToken,
		})
		cancel()
		drv, err = hc, attachErr
		if err == nil && st.Busy && st.TurnID != "" {
			ag.attachedTurnID = st.TurnID
			ag.focusConvID = st.FocusConvID
		}
		if err == nil && st.Busy {
			if bin, binErr := agenthost.SpawnBinary(d.cfg.SumiBinary); binErr == nil {
				ag.staleBuild = agenthost.BuildStale(st.BuildHash, bin)
			}
		}
	} else {
		drv, err = agenthost.DefaultDriverBuilder(spec, ag.homeDir)
	}
	if err != nil {
		code, detail := failureOf(err, spec)
		if code == domain.AgentErrTurnFailed {
			code = domain.AgentErrDriverStartFailed
		}
		return nil, code, detail
	}
	return drv, "", ""
}

// failureOf maps a driver error to an agent error code and a bounded, secret-free detail.
func failureOf(err error, spec protocol.AssignedAgentSpec) (string, string) {
	code := domain.AgentErrTurnFailed
	var f *driver.Failure
	if errors.As(err, &f) && f.Code != "" {
		code = f.Code
	}
	detail := err.Error()
	if key := spec.DriverConfig.APIKey; key != "" {
		detail = strings.ReplaceAll(detail, key, "[redacted]")
	}
	return code, domain.BoundErrorDetail(detail)
}

// agentEnv builds the child environment: `sumi` first on PATH, capability socket credentials,
// HOME pointed at Agent Home for builtin, and per-agent isolated provider config dirs for ACP.
// Inherited SUMI_* variables and anything carrying the Computer token are dropped so an
// Agent can only reach the Server through its own driver token.
func (d *Daemon) agentEnv(ag *hostedAgent, kind domain.DriverKind, provider string) []string {
	d.mu.RLock()
	binDir := d.binDir
	d.mu.RUnlock()
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SUMI_") {
			continue
		}
		if tok := d.cfg.ComputerToken; tok != "" && strings.Contains(kv, tok) {
			continue
		}
		env = append(env, kv)
	}
	path := os.Getenv("PATH")
	if binDir != "" {
		path = binDir + string(os.PathListSeparator) + path
	}
	env = setEnv(env, "PATH", path)
	env = setEnv(env, "SUMI_SOCKET", d.SocketPath())
	env = setEnv(env, "SUMI_DRIVER_TOKEN", ag.driverToken)
	env = setEnv(env, "SUMI_ATTACHMENT_DIR", filepath.Join(ag.workDir, "attachments"))
	env = setEnv(env, "SUMI_DRAFT_DIR", filepath.Join(ag.homeDir, "drafts"))
	switch kind {
	case domain.DriverBuiltin:
		env = setEnv(env, "HOME", ag.homeDir)
	case domain.DriverACP:
		if runtimeDir := acpRuntimeDir(ag.homeDir, provider); runtimeDir != "" {
			switch provider {
			case domain.ACPProviderPi:
				env = setEnv(env, "PI_CODING_AGENT_DIR", runtimeDir)
			case domain.ACPProviderCodex:
				env = setEnv(env, "CODEX_HOME", runtimeDir)
			case domain.ACPProviderClaude:
				env = unsetEnv(env, "CLAUDECODE")
				env = setEnv(env, "CLAUDE_CONFIG_DIR", runtimeDir)
				env = setEnv(env, "CLAUDE_CODE_DISABLE_AUTO_MEMORY", "1")
				env = setEnv(env, "CLAUDE_CODE_DISABLE_CLAUDE_MDS", "1")
			}
		}
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, prefix+value)
}

func unsetEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

func (d *Daemon) handleControl(ctx context.Context, ctrl protocol.AgentControlFrame) {
	switch ctrl.Action {
	case "provision":
		if ctrl.Spec != nil {
			d.provisionAndWake(ctx, *ctrl.Spec, "provisioned")
		}
	case "reset_session":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag == nil {
			return
		}
		ag.mu.Lock()
		drv := ag.drv
		ag.pendingRetry = nil
		ag.resetBreakerLocked()
		ag.mu.Unlock()
		if drv != nil {
			_ = drv.ResetSession()
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "", "")
			d.scheduleWake(ctx, ctrl.AgentMemberID, "", "reset_session")
		}
	case "restart":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag != nil {
			ag.mu.Lock()
			if !ag.busy && ag.spec.MemberID != "" && (ag.drv == nil || !driverHealthy(ag.drv)) {
				old := ag.drv
				drv, code, detail := d.buildAgentDriver(ag, ag.spec)
				ag.drv, ag.failCode, ag.failDetail = drv, code, detail
				if old != nil {
					if oldHC, ok := old.(*agenthost.Client); ok && drv != nil {
						oldHC.Detach()
					} else {
						go func() { _ = old.Close() }()
					}
				}
			}
			ok, failCode, failDetail := ag.drv != nil, ag.failCode, ag.failDetail
			ag.resetBreakerLocked()
			ag.mu.Unlock()
			if !ok {
				d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeError, "", failCode, failDetail)
				return
			}
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "", "")
			d.scheduleWake(ctx, ctrl.AgentMemberID, "", "restarted")
		}
	case "stop_turn":
		d.mu.RLock()
		ag := d.agents[ctrl.AgentMemberID]
		d.mu.RUnlock()
		if ag == nil {
			return
		}
		ag.mu.Lock()
		cancelTurn := ag.cancelTurn
		wasBusy := ag.busy
		drvOK := ag.drv != nil && ag.failStreak == 0 && !ag.paused
		if wasBusy {
			ag.dirty = false
			ag.stopRequested = true
			ag.pendingRetry = nil
		} else {
			ag.pendingRetry = nil
		}
		ag.mu.Unlock()
		if cancelTurn != nil {
			cancelTurn(errTurnStopped)
		} else if !wasBusy && drvOK {
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "", "")
		}
	}
}

// scheduleWake enforces at most one active turn per Agent; concurrent wakes coalesce into dirty=true.
func (d *Daemon) scheduleWake(ctx context.Context, agentID, convID, reason string) {
	d.mu.RLock()
	ag := d.agents[agentID]
	d.mu.RUnlock()
	if ag == nil {
		return
	}
	ag.mu.Lock()
	if ag.drv == nil {
		code, detail := ag.failCode, ag.failDetail
		ag.mu.Unlock()
		d.reportAgentState(agentID, domain.RuntimeError, "", code, detail)
		return
	}
	if ag.paused {
		ag.mu.Unlock()
		return
	}
	ag.lastReason = reason
	if convID != "" {
		ag.lastConvID = convID
	}
	if d.isDraining() {
		ag.dirty = true
		ag.mu.Unlock()
		return
	}
	if ag.busy || (ag.retryTimer != nil && reason != "retry") {
		ag.dirty = true
		if ag.busy && ag.retryTimer == nil {
			d.armBusySteerLocked(ctx, ag)
		}
		ag.mu.Unlock()
		return
	}
	ag.busy = true
	ag.dirty = false
	ag.stopRequested = false
	ag.mu.Unlock()

	go d.runAgentLoop(ctx, ag)
}

// armBusySteerLocked debounces mid-turn inbox notices while a turn is active. Caller holds ag.mu.
func (d *Daemon) armBusySteerLocked(ctx context.Context, ag *hostedAgent) {
	if !ag.busy || ag.stopRequested || ag.steerTimer != nil || ag.drv == nil {
		return
	}
	if _, isSidecar := ag.drv.(*agenthost.Client); !isSidecar {
		if _, ok := ag.drv.(driver.Steerer); !ok {
			return
		}
	}
	delay := d.steerDebounce
	if delay <= 0 {
		delay = 1500 * time.Millisecond
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		ag.mu.Lock()
		if ag.steerTimer != timer {
			ag.mu.Unlock()
			return
		}
		ag.steerTimer = nil
		ag.mu.Unlock()
		d.trySteerBusyAgent(ctx, ag)
	})
	ag.steerTimer = timer
}

func steerFingerprint(items []domain.InboxItem) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s:%d:%d", it.ConversationID, it.LatestSeq, it.UnreadCount))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// trySteerBusyAgent fetches read-only unread attention items and injects a deduplicated mid-turn
// notice into drivers that support Steer. Failures are non-fatal; ag.dirty still guarantees drain.
func (d *Daemon) trySteerBusyAgent(ctx context.Context, ag *hostedAgent) {
	ag.mu.Lock()
	if !ag.busy || ag.stopRequested || ag.drv == nil || d.isDraining() {
		ag.mu.Unlock()
		return
	}
	agentID := ag.spec.MemberID
	drv := ag.drv
	turnID := ag.activeTurnID
	focusConvID := ag.focusConvID
	focusSeq := ag.focusLatestSeq
	prevFP := ag.lastSteerFP
	ag.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	items, err := d.fetchAgentInbox(fetchCtx, agentID)
	cancel()
	if err != nil || len(items) == 0 {
		return
	}
	relevant := make([]domain.InboxItem, 0, len(items))
	for _, it := range items {
		if it.ConversationID != focusConvID {
			relevant = append(relevant, it)
		} else if focusSeq == 0 || it.LatestSeq > focusSeq {
			relevant = append(relevant, it)
		}
	}
	if len(relevant) == 0 {
		return
	}
	relevant = orderInboxItems(relevant, nil)
	fp := steerFingerprint(relevant)
	if fp == "" || fp == prevFP {
		return
	}
	notice := buildSteerNotice(focusConvID, relevant)
	if notice == "" {
		return
	}

	ag.mu.Lock()
	stillSame := ag.busy && !ag.stopRequested && ag.drv == drv && ag.activeTurnID == turnID
	ag.mu.Unlock()
	if !stillSame {
		return
	}

	steerCtx, steerCancel := context.WithTimeout(ctx, 5*time.Second)
	defer steerCancel()
	if hc, ok := drv.(*agenthost.Client); ok {
		err = hc.SteerTurn(steerCtx, turnID, notice)
	} else if st, ok := drv.(driver.Steerer); ok {
		err = st.Steer(steerCtx, notice)
	} else {
		return
	}
	if err != nil {
		if !errors.Is(err, driver.ErrSteerUnsupported) && !errors.Is(err, driver.ErrTurnNotActive) {
			d.logger.Debug("Mid-turn steer skipped", "agent_id", agentID, "turn_id", turnID, "error", err.Error())
		}
		return
	}
	ag.mu.Lock()
	if ag.busy && ag.activeTurnID == turnID {
		ag.lastSteerFP = fp
	}
	ag.mu.Unlock()
	d.logger.Info("Mid-turn inbox notice steered", "agent_id", agentID, "turn_id", turnID, "items", len(relevant))
}

type silentNudge struct {
	focus domain.InboxItem
	held  bool
}

// orderInboxItems drops items that already failed to advance read_seq and orders the rest FIFO
// (oldest LatestAt first) so earlier conversations are never starved by newer activity elsewhere.
func orderInboxItems(items []domain.InboxItem, skipped map[string]int64) []domain.InboxItem {
	out := make([]domain.InboxItem, 0, len(items))
	for _, it := range items {
		if skipSeq, ok := skipped[it.ConversationID]; ok && it.LatestSeq <= skipSeq {
			continue
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := out[i].LatestAt, out[j].LatestAt
		if ti.IsZero() != tj.IsZero() {
			return !ti.IsZero()
		}
		if ti.IsZero() {
			return false
		}
		return ti.Before(tj)
	})
	return out
}

func (d *Daemon) runAgentLoop(ctx context.Context, ag *hostedAgent) {
	released := false
	defer func() {
		if !released {
			ag.mu.Lock()
			ag.busy = false
			ag.focusConvID = ""
			ag.focusLatestSeq = 0
			ag.activeTurnID = ""
			ag.cancelTurn = nil
			ag.activeTelemetry = nil
			ag.stopRequested = false
			ag.stopSteerTimerLocked()
			ag.mu.Unlock()
			d.notifyDrainCheck()
		}
	}()

	skipped := make(map[string]int64)
	unreadNudged := make(map[string]int64)
	var pendingNudge *silentNudge
	var prevConvID string
	var prevSeq int64

	for {
		d.replaceStaleSidecar(ag)
		ag.mu.Lock()
		spec, drv := ag.spec, ag.drv
		failCode, failDetail := ag.failCode, ag.failDetail
		ag.mu.Unlock()
		if drv == nil {
			d.reportAgentState(spec.MemberID, domain.RuntimeError, "", failCode, failDetail)
			return
		}
		if d.isDraining() {
			ag.mu.Lock()
			ag.dirty = true
			ag.busy = false
			ag.focusConvID = ""
			released = true
			ag.mu.Unlock()
			d.notifyDrainCheck()
			d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
			return
		}

		var focus domain.InboxItem
		var userPrompt string
		var inboxCount int
		var isNudge, isRecovery bool

		if pendingNudge != nil {
			nudge := pendingNudge
			pendingNudge = nil
			focus = nudge.focus
			inboxCount = 1
			isNudge = true
			userPrompt = buildSilentReplyNudgePrompt(focus, nudge.held)
		} else {
			items, err := d.fetchAgentInbox(ctx, spec.MemberID)
			if err != nil {
				d.reportAgentState(spec.MemberID, domain.RuntimeError, "", domain.AgentErrInboxUnreachable, domain.BoundErrorDetail(err.Error()))
				return
			}

			// Detect when the previous normal turn finished without advancing read_seq on its focus.
			if prevConvID != "" {
				for _, it := range items {
					if it.ConversationID == prevConvID && it.LatestSeq == prevSeq {
						if prevSeq == 0 || unreadNudged[prevConvID] == prevSeq {
							// Already nudged once (or mock inbox without seq): skip to avoid spinning.
							skipped[prevConvID] = prevSeq
						} else {
							unreadNudged[prevConvID] = prevSeq
						}
						break
					}
				}
				prevConvID, prevSeq = "", 0
			}

			var recovery *retryContext
			ag.mu.Lock()
			if ag.pendingRetry != nil {
				foundFresh := false
				for _, it := range items {
					if it.ConversationID == ag.pendingRetry.focus.ConversationID {
						foundFresh = true
						break
					}
				}
				if foundFresh {
					ag.pendingRetry = nil
				} else {
					cp := *ag.pendingRetry
					recovery = &cp
				}
			}
			ag.mu.Unlock()

			ordered := orderInboxItems(items, skipped)
			if recovery != nil {
				focus = recovery.focus
				inboxCount = len(ordered) + 1
				isRecovery = true
				userPrompt = buildRetryWakePrompt(focus, recovery.errCode, ordered)
			} else {
				if len(ordered) == 0 {
					// Check dirty and clear busy under the same lock so wakes arriving during fetchAgentInbox are never lost.
					ag.mu.Lock()
					if ag.dirty && !ag.paused && !d.isDraining() {
						ag.dirty = false
						ag.mu.Unlock()
						clear(skipped)
						continue
					}
					ag.busy = false
					ag.focusConvID = ""
					released = true
					ag.mu.Unlock()
					d.notifyDrainCheck()
					d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
					return
				}
				focus = ordered[0]
				inboxCount = len(ordered)
				if focus.LatestSeq > 0 && unreadNudged[focus.ConversationID] == focus.LatestSeq {
					isNudge = true
					userPrompt = buildUnreadNudgePrompt(focus)
				} else {
					userPrompt = buildWakePrompt(ordered)
				}
			}
		}

		tel := newTurnTelemetry()
		turnID := uuid.NewString()
		ag.mu.Lock()
		ag.focusConvID = focus.ConversationID
		ag.focusLatestSeq = focus.LatestSeq
		ag.activeTurnID = turnID
		ag.activeTelemetry = tel
		ag.stopSteerTimerLocked()
		ag.mu.Unlock()
		turnStart := time.Now()
		d.logger.Info("Agent turn started", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "inbox_items", inboxCount, "reason", focus.Reason, "nudge", isNudge, "recovery", isRecovery)
		d.reportAgentState(spec.MemberID, domain.RuntimeWorking, focus.ConversationID, "", "")

		turnCtx, cancel := context.WithCancelCause(ctx)
		stopDetachWatch := d.watchDetach(cancel)
		tracker := newWatchedTurnActivity(d.activity, func(a *domain.AgentActivity) {
			d.reportAgentStateWith(spec.MemberID, domain.RuntimeWorking, focus.ConversationID, "", "", a)
		}, func() bool {
			ag.mu.Lock()
			defer ag.mu.Unlock()
			return ag.dirty
		}, func() { cancel(errTurnStalled) })
		if spec.DriverKind == domain.DriverACP && spec.DriverConfig.Provider != "" {
			if err := prepareACPRuntimeDir(ag.homeDir, spec.DriverConfig); err != nil {
				d.logger.Warn("Refresh ACP runtime dir failed", "agent_id", spec.MemberID, "error", err.Error())
			}
		}
		if rebuilt, err := ensureAgentHome(ag); err != nil {
			d.logger.Warn("Restore agent home failed", "agent_id", spec.MemberID, "error", err.Error())
		} else if rebuilt {
			d.logger.Warn("Agent home was missing and has been rebuilt", "agent_id", spec.MemberID)
		}
		turnInput := driver.TurnInput{
			SystemPrompt:   buildSystemPrompt(spec, ag.workDir, ag.memoryDir),
			StartupContext: buildStartupContext(spec, ag.memoryDir),
			UserPrompt:     userPrompt,
			WorkDir:        ag.workDir,
			MemoryDir:      ag.memoryDir,
			Env:            d.agentEnv(ag, spec.DriverKind, spec.DriverConfig.Provider),
			OnActivity:     tracker.Observe,
		}
		ag.mu.Lock()
		if ag.stopRequested {
			ag.stopRequested = false
			ag.busy = false
			ag.focusConvID = ""
			ag.activeTelemetry = nil
			ag.dirty = false
			ag.pendingRetry = nil
			released = true
			ag.mu.Unlock()
			stopDetachWatch()
			cancel(nil)
			tracker.Close()
			d.logger.Info("Agent turn stopped", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "duration_ms", time.Since(turnStart).Milliseconds())
			d.notifyDrainCheck()
			d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
			return
		}
		ag.cancelTurn = cancel
		ag.mu.Unlock()
		var turnRes driver.TurnResult
		var runErr error
		if hc, ok := drv.(*agenthost.Client); ok {
			turnRes, runErr = hc.RunTurnWithID(turnCtx, turnID, focus.ConversationID, turnInput)
			if runErr != nil && isSidecarDialError(runErr) && turnCtx.Err() == nil {
				if recovered, recErr := d.recoverDeadSidecar(ag, hc, spec); recovered != nil {
					drv = recovered
					turnRes, runErr = recovered.RunTurnWithID(turnCtx, turnID, focus.ConversationID, turnInput)
				} else if recErr != nil {
					runErr = recErr
				}
			}
		} else {
			turnRes, runErr = drv.RunTurn(turnCtx, turnInput)
		}
		cause := context.Cause(turnCtx)
		if errors.Is(cause, errTurnStalled) {
			runErr = driver.Fail(domain.AgentErrTurnStalled, errTurnStalled)
		}
		stopDetachWatch()
		cancel(nil)
		tracker.Close()

		ag.mu.Lock()
		ag.cancelTurn = nil
		ag.activeTurnID = ""
		ag.focusLatestSeq = 0
		ag.activeTelemetry = nil
		ag.stopSteerTimerLocked()
		stopped := ag.stopRequested || errors.Is(cause, errTurnStopped)
		ag.stopRequested = false
		replaced := ag.drv != drv
		ag.mu.Unlock()

		if errors.Is(runErr, agenthost.ErrDetaching) || errors.Is(cause, agenthost.ErrDetaching) {
			// Daemon is detaching for hot upgrade; the turn continues inside agent-host.
			released = true
			return
		}

		if replaced {
			// Re-provisioned mid-turn: the old driver's outcome is moot; continue with the new one.
			continue
		}
		if stopped {
			ag.mu.Lock()
			ag.busy = false
			ag.focusConvID = ""
			ag.dirty = false
			ag.pendingRetry = nil
			released = true
			ag.mu.Unlock()
			d.logger.Info("Agent turn stopped", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "duration_ms", time.Since(turnStart).Milliseconds())
			d.notifyDrainCheck()
			d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
			return
		}
		if runErr != nil {
			code, detail := failureOf(runErr, spec)
			// If `sumi read` already advanced read_seq before the failure and no reply was sent,
			// preserve the focus so retry/probe resumes this conversation instead of seeing an empty inbox.
			if (tel.didRead(focus.ConversationID) || isRecovery) && !tel.didReply(focus.ConversationID) {
				ag.mu.Lock()
				ag.pendingRetry = &retryContext{focus: focus, errCode: code}
				ag.mu.Unlock()
			}
			d.logger.Warn("Agent turn failed", "agent_id", spec.MemberID, "error_code", code, "error", detail)
			if delay, paused := d.recordFailure(ctx, ag); paused {
				d.logger.Warn("Agent paused after consecutive failures", "agent_id", spec.MemberID, "error_code", code, "probe_in", delay.String())
				detail = domain.BoundErrorDetail(fmt.Sprintf("paused after %d failed turns, retry in %s: %s", d.breaker.threshold, delay.Round(time.Second), detail))
			} else {
				delay := d.armRetry(ctx, ag)
				detail = domain.BoundErrorDetail(fmt.Sprintf("retrying in %s: %s", delay.Round(time.Second), detail))
			}
			d.reportAgentState(spec.MemberID, domain.RuntimeError, focus.ConversationID, code, detail)
			return
		}

		ag.mu.Lock()
		ag.resetBreakerLocked()
		if ag.pendingRetry != nil && ag.pendingRetry.focus.ConversationID == focus.ConversationID {
			ag.pendingRetry = nil
		}
		ag.focusConvID = ""
		paused := ag.paused
		ag.mu.Unlock()
		d.logger.Info("Agent turn completed", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "duration_ms", time.Since(turnStart).Milliseconds())

		if paused || d.isDraining() {
			ag.mu.Lock()
			ag.busy = false
			ag.focusConvID = ""
			released = true
			ag.mu.Unlock()
			d.notifyDrainCheck()
			d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
			return
		}

		// Catch turns where the agent read the focus conversation (or hit Freshness Guard `held`)
		// and stopped without posting via `sumi send` or reacting via `sumi react`.
		readFocus := tel.didRead(focus.ConversationID)
		repliedFocus := tel.didReply(focus.ConversationID)
		heldUnresolved := tel.hasUnresolvedHeld(focus.ConversationID)
		if !isNudge && (readFocus || isRecovery) && !repliedFocus && (heldUnresolved || focus.Reason == "dm" || focus.Reason == "mention" || turnRes.ToolCalls >= 2) {
			pendingNudge = &silentNudge{focus: focus, held: heldUnresolved}
			continue
		}

		// Remember the just-handled conversation so the next inbox drain can detect zero progress.
		if !isRecovery {
			prevConvID, prevSeq = focus.ConversationID, focus.LatestSeq
		}
		ag.mu.Lock()
		ag.dirty = false
		ag.mu.Unlock()
	}
}

func (d *Daemon) watchDetach(cancel context.CancelCauseFunc) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-d.detachCh:
			cancel(agenthost.ErrDetaching)
		case <-done:
		}
	}()
	return func() { close(done) }
}

// resumeAttachedTurn re-attaches to a turn already running inside a surviving `sumi agent-host`
// sidecar after a hot `sumi computer` restart, then continues the normal agent loop if dirty.
func (d *Daemon) resumeAttachedTurn(ctx context.Context, ag *hostedAgent, turnID, focusConvID string) {
	ag.mu.Lock()
	spec, drv := ag.spec, ag.drv
	ag.mu.Unlock()
	hc, ok := drv.(*agenthost.Client)
	if !ok {
		ag.mu.Lock()
		ag.busy = false
		ag.focusConvID = ""
		ag.mu.Unlock()
		d.notifyDrainCheck()
		return
	}

	turnStart := time.Now()
	d.logger.Info("Re-attached to in-flight agent turn", "agent_id", spec.MemberID, "turn_id", turnID, "conversation_id", focusConvID)
	turnCtx, cancel := context.WithCancelCause(ctx)
	stopDetachWatch := d.watchDetach(cancel)
	tracker := newWatchedTurnActivity(d.activity, func(a *domain.AgentActivity) {
		d.reportAgentStateWith(spec.MemberID, domain.RuntimeWorking, focusConvID, "", "", a)
	}, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return ag.dirty
	}, func() { cancel(errTurnStalled) })

	ag.mu.Lock()
	if ag.stopRequested {
		ag.stopRequested = false
		ag.busy = false
		ag.focusConvID = ""
		ag.dirty = false
		ag.mu.Unlock()
		stopDetachWatch()
		cancel(nil)
		tracker.Close()
		hc.CancelTurn(turnID)
		d.logger.Info("Re-attached agent turn stopped", "agent_id", spec.MemberID, "turn_id", turnID, "duration_ms", time.Since(turnStart).Milliseconds())
		d.notifyDrainCheck()
		d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
		return
	}
	ag.cancelTurn = cancel
	ag.activeTurnID = turnID
	ag.stopSteerTimerLocked()
	ag.mu.Unlock()

	_, runErr := hc.WaitTurn(turnCtx, turnID, tracker.Observe)
	cause := context.Cause(turnCtx)
	if errors.Is(cause, errTurnStalled) {
		runErr = driver.Fail(domain.AgentErrTurnStalled, errTurnStalled)
	}
	stopDetachWatch()
	cancel(nil)
	tracker.Close()

	ag.mu.Lock()
	ag.cancelTurn = nil
	ag.activeTurnID = ""
	ag.stopSteerTimerLocked()
	stopped := ag.stopRequested || errors.Is(cause, errTurnStopped)
	ag.stopRequested = false
	ag.mu.Unlock()

	if errors.Is(runErr, agenthost.ErrDetaching) || errors.Is(cause, agenthost.ErrDetaching) {
		return
	}

	if stopped {
		ag.mu.Lock()
		ag.busy = false
		ag.focusConvID = ""
		ag.dirty = false
		ag.mu.Unlock()
		d.logger.Info("Re-attached agent turn stopped", "agent_id", spec.MemberID, "turn_id", turnID, "duration_ms", time.Since(turnStart).Milliseconds())
		d.notifyDrainCheck()
		d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
		return
	}

	if runErr != nil {
		code, detail := failureOf(runErr, spec)
		d.logger.Warn("Re-attached agent turn failed", "agent_id", spec.MemberID, "error_code", code, "error", detail)
		if delay, paused := d.recordFailure(ctx, ag); paused {
			detail = domain.BoundErrorDetail(fmt.Sprintf("paused after %d failed turns, retry in %s: %s", d.breaker.threshold, delay.Round(time.Second), detail))
		} else {
			delay := d.armRetry(ctx, ag)
			detail = domain.BoundErrorDetail(fmt.Sprintf("retrying in %s: %s", delay.Round(time.Second), detail))
		}
		ag.mu.Lock()
		ag.busy = false
		ag.focusConvID = ""
		ag.mu.Unlock()
		d.notifyDrainCheck()
		d.reportAgentState(spec.MemberID, domain.RuntimeError, focusConvID, code, detail)
		return
	}

	ag.mu.Lock()
	ag.resetBreakerLocked()
	ag.focusConvID = ""
	shouldContinue := !ag.paused && !d.isDraining()
	if !shouldContinue {
		ag.busy = false
	} else {
		ag.dirty = false
	}
	ag.mu.Unlock()

	d.logger.Info("Re-attached agent turn completed", "agent_id", spec.MemberID, "turn_id", turnID, "duration_ms", time.Since(turnStart).Milliseconds())
	if !shouldContinue {
		d.notifyDrainCheck()
		d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
		return
	}
	d.runAgentLoop(ctx, ag)
}

// recordFailure counts a failed turn and, at the threshold, pauses the Agent and arms one probe
// wake. Each failure while at the threshold doubles the probe delay up to maxProbe.
func (d *Daemon) recordFailure(ctx context.Context, ag *hostedAgent) (time.Duration, bool) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	ag.failStreak++
	if ag.failStreak < d.breaker.threshold {
		return 0, false
	}
	switch {
	case ag.probeDelay == 0:
		ag.probeDelay = d.breaker.firstProbe
	case ag.probeDelay < d.breaker.maxProbe:
		ag.probeDelay = min(ag.probeDelay*2, d.breaker.maxProbe)
	}
	ag.paused = true
	if ag.probeTimer != nil {
		ag.probeTimer.Stop()
	}
	agentID, delay := ag.spec.MemberID, ag.probeDelay
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		ag.mu.Lock()
		if ag.probeTimer != timer {
			ag.mu.Unlock()
			return
		}
		ag.paused, ag.probeTimer = false, nil
		ag.mu.Unlock()
		d.scheduleWake(ctx, agentID, "", "probe")
	})
	ag.probeTimer = timer
	return delay, true
}

// armRetry schedules one re-run of the loop after a failed turn below the breaker threshold, so
// triggers queued during the failed turn are redelivered without waiting for another wake.
func (d *Daemon) armRetry(ctx context.Context, ag *hostedAgent) time.Duration {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	delay := d.breaker.firstRetry
	for i := 1; i < ag.failStreak && delay < d.breaker.maxRetry; i++ {
		delay *= 2
	}
	delay = min(delay, d.breaker.maxRetry)
	// ±20% jitter keeps many Agents failing on the same provider from retrying in lockstep.
	delay += time.Duration((mrand.Float64()*0.4 - 0.2) * float64(delay))
	if ag.retryTimer != nil {
		ag.retryTimer.Stop()
	}
	agentID := ag.spec.MemberID
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		ag.mu.Lock()
		if ag.retryTimer != timer {
			ag.mu.Unlock()
			return
		}
		ag.retryTimer = nil
		ag.mu.Unlock()
		d.scheduleWake(ctx, agentID, "", "retry")
	})
	ag.retryTimer = timer
	d.logger.Info("Agent retry scheduled", "agent_id", agentID, "fail_streak", ag.failStreak, "delay_ms", delay.Milliseconds())
	return delay
}

func driverHealthy(drv driver.SessionDriver) bool {
	hc, ok := drv.(*agenthost.Client)
	if !ok {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := hc.Status(ctx)
	return err == nil
}

func isSidecarDialError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// replaceStaleSidecar swaps a sidecar flagged staleBuild for one spawned from the current binary.
// It runs at the top of each agent-loop iteration, so the sidecar's previous turn has ended and
// AttachOrSpawn retires it through the graceful shutdown path instead of re-attaching.
func (d *Daemon) replaceStaleSidecar(ag *hostedAgent) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if !ag.staleBuild || ag.stopRequested || d.isDraining() {
		return
	}
	old, ok := ag.drv.(*agenthost.Client)
	if !ok {
		ag.staleBuild = false
		return
	}
	drv, code, detail := d.buildAgentDriver(ag, ag.spec)
	ag.drv, ag.failCode, ag.failDetail = drv, code, detail
	if drv == nil {
		d.logger.Warn("Replace stale-build agent-host sidecar failed", "agent_id", ag.spec.MemberID, "error_code", code)
		go func() { _ = old.Close() }()
		return
	}
	// AttachOrSpawn already shut the old sidecar down; Close would hit its replacement.
	old.Detach()
	d.logger.Info("Replaced agent-host sidecar running an older build", "agent_id", ag.spec.MemberID)
}

// recoverDeadSidecar respawns a missing agent-host sidecar before failing a turn whose
// initial dial never reached any process.
func (d *Daemon) recoverDeadSidecar(ag *hostedAgent, dead *agenthost.Client, spec protocol.AssignedAgentSpec) (*agenthost.Client, error) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.drv != dead || ag.stopRequested || d.isDraining() {
		return nil, nil
	}
	drv, code, detail := d.buildAgentDriver(ag, spec)
	if drv == nil {
		return nil, driver.Fail(code, errors.New(detail))
	}
	hc, ok := drv.(*agenthost.Client)
	if !ok {
		_ = drv.Close()
		return nil, nil
	}
	ag.drv = hc
	dead.Detach()
	d.logger.Warn("Respawned dead agent-host sidecar", "agent_id", spec.MemberID)
	return hc, nil
}

func (d *Daemon) fetchAgentInbox(ctx context.Context, agentID string) ([]domain.InboxItem, error) {
	url := strings.TrimRight(d.cfg.ServerURL, "/") + "/api/v1/computer/agents/" + agentID + "/inbox?filter=needs_me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+d.cfg.ComputerToken)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("inbox status %d", resp.StatusCode)
	}
	var out struct {
		Items []domain.InboxItem `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Items, nil
}
