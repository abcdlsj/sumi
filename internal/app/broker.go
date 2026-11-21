package app

import (
	"context"
	"sync"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
)

// EventHub broadcasts committed SpaceEvents to in-process SSE subscribers.
type EventHub struct {
	mu   sync.RWMutex
	subs map[string]map[chan domain.SpaceEvent]struct{}
}

func NewEventHub() *EventHub {
	return &EventHub{
		subs: make(map[string]map[chan domain.SpaceEvent]struct{}),
	}
}

func (h *EventHub) Subscribe(spaceID string) (<-chan domain.SpaceEvent, func()) {
	ch := make(chan domain.SpaceEvent, 64)
	h.mu.Lock()
	if h.subs[spaceID] == nil {
		h.subs[spaceID] = make(map[chan domain.SpaceEvent]struct{})
	}
	h.subs[spaceID][ch] = struct{}{}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		if set, ok := h.subs[spaceID]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, spaceID)
			}
		}
		h.mu.Unlock()
	}
	return ch, cancel
}

func (h *EventHub) Publish(ev domain.SpaceEvent) {
	h.mu.RLock()
	set := h.subs[ev.SpaceID]
	for ch := range set {
		select {
		case ch <- ev:
		default:
			// Slow subscriber drops live frame and catches up via sequence replay.
		}
	}
	h.mu.RUnlock()
}

// ComputerNotifier delivers post-commit wake and control frames to connected Computers.
type ComputerNotifier interface {
	SendWake(computerID string, frame protocol.WakeFrame)
	SendControl(computerID string, frame protocol.AgentControlFrame)
}

type noopNotifier struct{}

func (noopNotifier) SendWake(string, protocol.WakeFrame)            {}
func (noopNotifier) SendControl(string, protocol.AgentControlFrame) {}

// WorkspaceClient fetches read-only agent workspace listings and files over the Computer WebSocket.
type WorkspaceClient interface {
	ListWorkspace(ctx context.Context, computerID, agentMemberID, relPath string, includeHidden bool) (*domain.WorkspaceListing, error)
	ReadWorkspaceFile(ctx context.Context, computerID, agentMemberID, relPath string) (*domain.WorkspaceFileContent, error)
}
