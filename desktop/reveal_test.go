package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowRevealerReadyBeatsFallback(t *testing.T) {
	var calls atomic.Int32
	r := newWindowRevealer(func() { calls.Add(1) })
	r.scheduleFallback(120 * time.Millisecond)
	defer r.stop()

	if !r.reveal() {
		t.Fatal("expected first reveal call to succeed")
	}
	if r.reveal() {
		t.Fatal("expected second reveal call to be ignored")
	}
	time.Sleep(160 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly one reveal callback, got %d", got)
	}
}

func TestWindowRevealerFallbackFiresWhenReadyMissing(t *testing.T) {
	done := make(chan struct{}, 1)
	var calls atomic.Int32
	r := newWindowRevealer(func() {
		calls.Add(1)
		select {
		case done <- struct{}{}:
		default:
		}
	})
	defer r.stop()

	r.scheduleFallback(25 * time.Millisecond)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected fallback timer to reveal window")
	}
	if r.reveal() {
		t.Fatal("expected late ready signal after fallback to be ignored")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected exactly one reveal callback, got %d", got)
	}
}

func TestWindowRevealerConcurrentRevealRunsOnce(t *testing.T) {
	var calls atomic.Int32
	r := newWindowRevealer(func() { calls.Add(1) })

	var wg sync.WaitGroup
	var winners atomic.Int32
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.reveal() {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()

	if winners.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("expected single winner/call, got winners=%d calls=%d", winners.Load(), calls.Load())
	}
}

func TestWindowRevealerStopCancelsFallback(t *testing.T) {
	var calls atomic.Int32
	r := newWindowRevealer(func() { calls.Add(1) })
	r.scheduleFallback(30 * time.Millisecond)
	r.stop()
	time.Sleep(60 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("expected stopped fallback not to fire, got %d", got)
	}
}
