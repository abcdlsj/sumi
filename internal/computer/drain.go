package computer

import (
	"context"
	"sort"
	"time"
)

const (
	// MaxDrainDeadline caps how long a drained upgrade waits for active turns before proceeding.
	MaxDrainDeadline = 10 * time.Minute
	// defaultDrainHoldGrace auto-releases a drain if the upgrader vanishes after the deadline.
	defaultDrainHoldGrace = 5 * time.Minute
)

// DrainOutcome summarizes how a computer upgrade drain completed.
type DrainOutcome struct {
	Finished    string    `json:"finished"` // "quiet" | "deadline" | "ended"
	DurationMs  int64     `json:"duration_ms"`
	WaitedFor   []string  `json:"waited_for,omitempty"`
	NotQuiet    []string  `json:"not_quiet,omitempty"`
	TotalAgents int       `json:"total_agents"`
	DeadlineAt  time.Time `json:"deadline_at"`
}

// DaemonStatus is the live snapshot served over computer.ctl.sock.
type DaemonStatus struct {
	Connected       bool      `json:"connected"`
	ConnectedAt     time.Time `json:"connected_at,omitempty"`
	SocketPath      string    `json:"socket_path,omitempty"`
	Draining        bool      `json:"draining"`
	DrainRequestID  string    `json:"drain_request_id,omitempty"`
	DrainDeadlineAt time.Time `json:"drain_deadline_at,omitempty"`
	HostedAgents    []string  `json:"hosted_agents"`
	BusyAgents      []string  `json:"busy_agents"`
	UseSidecar      bool      `json:"use_sidecar"`
}

func (d *Daemon) isDraining() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.draining
}

func (d *Daemon) notifyDrainCheck() {
	select {
	case d.drainCheckCh <- struct{}{}:
	default:
	}
}

func (d *Daemon) clearDrainTimerLocked() {
	d.mu.Lock()
	if d.drainHoldTimer != nil {
		d.drainHoldTimer.Stop()
		d.drainHoldTimer = nil
	}
	d.mu.Unlock()
}

// busyAgentIDs returns hosted agent IDs that are currently NOT quiet.
// An agent without a working driver or paused by the failure breaker is considered quiet
// because it cannot complete a model turn before the deadline.
func (d *Daemon) busyAgentIDs() (total int, busy []string) {
	d.mu.RLock()
	agents := make([]*hostedAgent, 0, len(d.agents))
	for _, ag := range d.agents {
		agents = append(agents, ag)
	}
	d.mu.RUnlock()

	total = len(agents)
	for _, ag := range agents {
		ag.mu.Lock()
		runnable := ag.drv != nil && !ag.paused
		isBusy := ag.busy && runnable
		id := ag.spec.MemberID
		ag.mu.Unlock()
		if isBusy {
			busy = append(busy, id)
		}
	}
	sort.Strings(busy)
	return total, busy
}

// Status returns a snapshot of the daemon's connection, hosted agents, and drain state.
func (d *Daemon) Status() DaemonStatus {
	d.mu.RLock()
	st := DaemonStatus{
		Connected:       d.connected,
		ConnectedAt:     d.connectedAt,
		SocketPath:      d.socketPath,
		Draining:        d.draining,
		DrainRequestID:  d.drainRequestID,
		DrainDeadlineAt: d.drainDeadlineAt,
		UseSidecar:      d.cfg.UseSidecar,
		HostedAgents:    make([]string, 0, len(d.agents)),
		BusyAgents:      []string{},
	}
	agents := make([]*hostedAgent, 0, len(d.agents))
	for id, ag := range d.agents {
		st.HostedAgents = append(st.HostedAgents, id)
		agents = append(agents, ag)
	}
	d.mu.RUnlock()

	sort.Strings(st.HostedAgents)
	for _, ag := range agents {
		ag.mu.Lock()
		if ag.busy && ag.drv != nil && !ag.paused {
			st.BusyAgents = append(st.BusyAgents, ag.spec.MemberID)
		}
		ag.mu.Unlock()
	}
	sort.Strings(st.BusyAgents)
	return st
}

// BeginDrain holds new agent wakes and waits until every runnable agent becomes quiet or
// deadline elapses. The hold remains active after returning "quiet" so no new turn starts
// before shutdown; callers must call EndDrain() if the upgrade is aborted.
func (d *Daemon) BeginDrain(ctx context.Context, requestID string, deadline time.Duration) DrainOutcome {
	if deadline < 0 {
		deadline = 0
	}
	if deadline > MaxDrainDeadline {
		deadline = MaxDrainDeadline
	}
	startedAt := time.Now()
	deadlineAt := startedAt.Add(deadline)

	d.mu.Lock()
	d.draining = true
	d.drainRequestID = requestID
	d.drainDeadlineAt = deadlineAt
	if d.drainHoldTimer != nil {
		d.drainHoldTimer.Stop()
	}
	grace := d.drainHoldGrace
	if grace <= 0 {
		grace = defaultDrainHoldGrace
	}
	// Hold-cap guard: if the caller crashes before stopping or calling EndDrain, auto-release.
	reqID := requestID
	d.drainHoldTimer = time.AfterFunc(deadline+grace, func() {
		d.mu.RLock()
		stillSame := d.draining && d.drainRequestID == reqID
		d.mu.RUnlock()
		if stillSame {
			d.logger.Warn("Upgrade drain hold cap expired; releasing held wakes", "request_id", reqID)
			d.EndDrain(context.Background())
		}
	})
	d.mu.Unlock()

	total, initialBusy := d.busyAgentIDs()
	if len(initialBusy) == 0 {
		return DrainOutcome{
			Finished:    "quiet",
			DurationMs:  time.Since(startedAt).Milliseconds(),
			TotalAgents: total,
			DeadlineAt:  deadlineAt,
		}
	}

	deadlineTimer := time.NewTimer(deadline)
	defer deadlineTimer.Stop()
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			t, rem := d.busyAgentIDs()
			return DrainOutcome{
				Finished:    "ended",
				DurationMs:  time.Since(startedAt).Milliseconds(),
				WaitedFor:   initialBusy,
				NotQuiet:    rem,
				TotalAgents: t,
				DeadlineAt:  deadlineAt,
			}
		case <-d.stopCh:
			t, rem := d.busyAgentIDs()
			return DrainOutcome{
				Finished:    "ended",
				DurationMs:  time.Since(startedAt).Milliseconds(),
				WaitedFor:   initialBusy,
				NotQuiet:    rem,
				TotalAgents: t,
				DeadlineAt:  deadlineAt,
			}
		case <-deadlineTimer.C:
			t, rem := d.busyAgentIDs()
			fin := "deadline"
			if len(rem) == 0 {
				fin = "quiet"
			}
			return DrainOutcome{
				Finished:    fin,
				DurationMs:  time.Since(startedAt).Milliseconds(),
				WaitedFor:   initialBusy,
				NotQuiet:    rem,
				TotalAgents: t,
				DeadlineAt:  deadlineAt,
			}
		case <-d.drainCheckCh:
		case <-poll.C:
		}

		if !d.isDraining() {
			t, rem := d.busyAgentIDs()
			return DrainOutcome{
				Finished:    "ended",
				DurationMs:  time.Since(startedAt).Milliseconds(),
				WaitedFor:   initialBusy,
				NotQuiet:    rem,
				TotalAgents: t,
				DeadlineAt:  deadlineAt,
			}
		}
		t, rem := d.busyAgentIDs()
		if len(rem) == 0 {
			return DrainOutcome{
				Finished:    "quiet",
				DurationMs:  time.Since(startedAt).Milliseconds(),
				WaitedFor:   initialBusy,
				TotalAgents: t,
				DeadlineAt:  deadlineAt,
			}
		}
	}
}

// EndDrain releases the drain hold and wakes any agents that accumulated dirty signals while held.
func (d *Daemon) EndDrain(ctx context.Context) {
	d.mu.Lock()
	wasDraining := d.draining
	d.draining = false
	d.drainRequestID = ""
	d.drainDeadlineAt = time.Time{}
	if d.drainHoldTimer != nil {
		d.drainHoldTimer.Stop()
		d.drainHoldTimer = nil
	}
	agentIDs := make([]string, 0, len(d.agents))
	for id, ag := range d.agents {
		ag.mu.Lock()
		if ag.dirty {
			agentIDs = append(agentIDs, id)
		}
		ag.mu.Unlock()
	}
	d.mu.Unlock()

	if !wasDraining {
		return
	}
	d.notifyDrainCheck()
	for _, id := range agentIDs {
		d.scheduleWake(ctx, id, "", "drain_released")
	}
}
