package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// windowRevealer coordinates cold-start first-frame reveal with a safety fallback timer so
// the hidden main window appears once styled content is painted and never stays invisible.
type windowRevealer struct {
	revealed atomic.Bool
	onReveal func()

	mu    sync.Mutex
	timer *time.Timer
}

func newWindowRevealer(onReveal func()) *windowRevealer {
	return &windowRevealer{onReveal: onReveal}
}

// reveal performs the initial reveal at most once and returns true if this call triggered it.
func (r *windowRevealer) reveal() bool {
	if r == nil || !r.revealed.CompareAndSwap(false, true) {
		return false
	}
	r.stop()
	if r.onReveal != nil {
		r.onReveal()
	}
	return true
}

func (r *windowRevealer) scheduleFallback(delay time.Duration) {
	if r == nil || r.revealed.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revealed.Load() {
		return
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(delay, func() {
		r.reveal()
	})
}

func (r *windowRevealer) stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	timer := r.timer
	r.timer = nil
	r.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}
