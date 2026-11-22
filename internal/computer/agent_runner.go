package computer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/driver/acp"
	"github.com/abcdlsj/sumi/internal/driver/builtin"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func (d *Daemon) provisionAndWake(ctx context.Context, spec protocol.AssignedAgentSpec, reason string) {
	ag := d.provisionAgent(spec)
	ag.mu.Lock()
	failCode, failDetail, ok, busy, focusConvID := ag.failCode, ag.failDetail, ag.drv != nil, ag.busy, ag.focusConvID
	if ok && busy {
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
	if ag.drv != nil && reflect.DeepEqual(ag.spec, spec) {
		return ag
	}
	reprovision := ag.drv != nil
	old := ag.drv
	drv, code, detail := d.buildAgentDriver(ag, spec)
	if drv != nil && reprovision {
		if err := drv.ResetSession(); err != nil {
			d.logger.Warn("Reset session on re-provision failed", "agent_id", spec.MemberID, "error", err.Error())
		}
	}
	ag.spec, ag.drv, ag.failCode, ag.failDetail = spec, drv, code, detail
	ag.resetBreakerLocked()
	if old != nil {
		// Closing may wait for an in-flight turn; never block provisioning on it.
		go func() { _ = old.Close() }()
	}
	return ag
}

func acpFingerprint(cfg domain.DriverConfig) string {
	return strings.Join([]string{
		"provider=" + cfg.Provider,
		"pi_provider=" + cfg.PiProvider,
		"pi_model=" + cfg.PiModel,
	}, ";")
}

func (d *Daemon) buildAgentDriver(ag *hostedAgent, spec protocol.AssignedAgentSpec) (driver.SessionDriver, string, string) {
	for _, dir := range []string{ag.workDir, ag.memoryDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, domain.AgentErrDriverStartFailed, domain.BoundErrorDetail(err.Error())
		}
	}
	if spec.DriverKind == domain.DriverACP && spec.DriverConfig.Provider != "" {
		if err := prepareACPRuntimeDir(ag.homeDir, spec.DriverConfig); err != nil {
			return nil, domain.AgentErrDriverStartFailed, domain.BoundErrorDetail(err.Error())
		}
	}
	memFile := filepath.Join(ag.memoryDir, "MEMORY.md")
	if _, err := os.Stat(memFile); os.IsNotExist(err) {
		_ = os.WriteFile(memFile, []byte(memoryTemplate), 0o600)
	}
	var drv driver.SessionDriver
	var err error
	if d.cfg.DriverFactory != nil {
		drv, err = d.cfg.DriverFactory(spec, ag.homeDir)
	} else {
		drv, err = buildDriver(spec, ag.homeDir)
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

func buildDriver(spec protocol.AssignedAgentSpec, homeDir string) (driver.SessionDriver, error) {
	cfg, err := domain.NormalizeDriverConfig(spec.DriverKind, spec.DriverConfig)
	if err != nil {
		var de *domain.Error
		if errors.As(err, &de) {
			err = errors.New(de.Message)
		}
		return nil, driver.Fail(domain.AgentErrDriverConfigInvalid, err)
	}
	switch spec.DriverKind {
	case domain.DriverBuiltin:
		return builtin.New(builtin.Config{
			BaseURL:     cfg.BaseURL,
			APIKey:      cfg.APIKey,
			Model:       cfg.Model,
			SessionFile: filepath.Join(homeDir, "session.jsonl"),
		}, nil)
	case domain.DriverACP:
		acfg := acp.Config{
			Provider:    cfg.Provider,
			Command:     cfg.Command,
			SessionFile: filepath.Join(homeDir, "acp-session.json"),
			Fingerprint: acpFingerprint(cfg),
		}
		argv, err := acfg.ResolvedCommand()
		if err != nil {
			return nil, driver.Fail(domain.AgentErrDriverConfigInvalid, err)
		}
		bins := []string{argv[0]}
		if len(cfg.Command) == 0 {
			bins = acp.ProviderBinaries(cfg.Provider)
		}
		for _, bin := range bins {
			if _, err := exec.LookPath(bin); err != nil {
				return nil, driver.Fail(domain.AgentErrDriverStartFailed, fmt.Errorf("acp command %q not found on PATH", bin))
			}
		}
		return acp.New(acfg), nil
	default:
		return nil, driver.Fail(domain.AgentErrDriverConfigInvalid, fmt.Errorf("unsupported driver kind %q", spec.DriverKind))
	}
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
			ag.resetBreakerLocked()
			ag.mu.Unlock()
			d.reportAgentState(ctrl.AgentMemberID, domain.RuntimeIdle, "", "", "")
			d.scheduleWake(ctx, ctrl.AgentMemberID, "", "restarted")
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
	if ag.busy || (ag.retryTimer != nil && reason != "retry") {
		ag.dirty = true
		ag.mu.Unlock()
		return
	}
	ag.busy = true
	ag.dirty = false
	ag.mu.Unlock()

	go d.runAgentLoop(ctx, ag)
}

func (d *Daemon) runAgentLoop(ctx context.Context, ag *hostedAgent) {
	released := false
	defer func() {
		if !released {
			ag.mu.Lock()
			ag.busy = false
			ag.focusConvID = ""
			ag.mu.Unlock()
		}
	}()
	for {
		ag.mu.Lock()
		spec, drv := ag.spec, ag.drv
		failCode, failDetail := ag.failCode, ag.failDetail
		ag.mu.Unlock()
		if drv == nil {
			d.reportAgentState(spec.MemberID, domain.RuntimeError, "", failCode, failDetail)
			return
		}

		items, err := d.fetchAgentInbox(ctx, spec.MemberID)
		if err != nil {
			d.reportAgentState(spec.MemberID, domain.RuntimeError, "", domain.AgentErrInboxUnreachable, domain.BoundErrorDetail(err.Error()))
			return
		}
		if len(items) == 0 {
			// Check dirty and clear busy under the same lock so wakes arriving during fetchAgentInbox are never lost.
			ag.mu.Lock()
			if ag.dirty && !ag.paused {
				ag.dirty = false
				ag.mu.Unlock()
				continue
			}
			ag.busy = false
			ag.focusConvID = ""
			released = true
			ag.mu.Unlock()
			d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")
			return
		}

		focus := items[0]
		ag.mu.Lock()
		ag.focusConvID = focus.ConversationID
		ag.mu.Unlock()
		turnStart := time.Now()
		d.logger.Info("Agent turn started", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "inbox_items", len(items), "reason", focus.Reason)
		d.reportAgentState(spec.MemberID, domain.RuntimeWorking, focus.ConversationID, "", "")

		turnCtx, cancel := context.WithCancelCause(ctx)
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
		_, runErr := drv.RunTurn(turnCtx, driver.TurnInput{
			SystemPrompt:   buildSystemPrompt(spec, ag.workDir, ag.memoryDir),
			StartupContext: buildStartupContext(spec, ag.memoryDir),
			UserPrompt:     buildWakePrompt(items),
			WorkDir:        ag.workDir,
			MemoryDir:      ag.memoryDir,
			Env:            d.agentEnv(ag, spec.DriverKind, spec.DriverConfig.Provider),
			OnActivity:     tracker.Observe,
		})
		if errors.Is(context.Cause(turnCtx), errTurnStalled) {
			runErr = driver.Fail(domain.AgentErrTurnStalled, errTurnStalled)
		}
		cancel(nil)
		tracker.Close()

		ag.mu.Lock()
		replaced := ag.drv != drv
		ag.mu.Unlock()
		if replaced {
			// Re-provisioned mid-turn: the old driver's outcome is moot; continue with the new one.
			continue
		}
		if runErr != nil {
			code, detail := failureOf(runErr, spec)
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
		ag.focusConvID = ""
		ag.mu.Unlock()
		d.logger.Info("Agent turn completed", "agent_id", spec.MemberID, "conversation_id", focus.ConversationID, "duration_ms", time.Since(turnStart).Milliseconds())
		d.reportAgentState(spec.MemberID, domain.RuntimeIdle, "", "", "")

		// Check if new wakes arrived while busy; clear busy under the same lock to prevent lost wakes.
		ag.mu.Lock()
		if !ag.dirty || ag.paused {
			ag.busy = false
			ag.focusConvID = ""
			released = true
			ag.mu.Unlock()
			return
		}
		ag.dirty = false
		ag.mu.Unlock()
	}
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
