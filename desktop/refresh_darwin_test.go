//go:build darwin

package main

import (
	"os"
	goruntime "runtime"
	"testing"
)

var mainThreadTasks = make(chan func())

func init() {
	goruntime.LockOSThread()
}

// TestMain keeps the process main OS thread available for WebKit/AppKit calls
// that assert isMainThread() while tests run on worker goroutines.
func TestMain(m *testing.M) {
	done := make(chan int, 1)
	go func() {
		done <- m.Run()
	}()
	for {
		select {
		case fn := <-mainThreadTasks:
			fn()
		case code := <-done:
			os.Exit(code)
		}
	}
}

func runOnMainThread(fn func()) {
	done := make(chan struct{})
	mainThreadTasks <- func() {
		defer close(done)
		fn()
	}
	<-done
}

func TestUnlockNativeWindowRefreshRate(t *testing.T) {
	if unlockNativeWindowRefreshRate(nil) {
		t.Fatal("expected nil window handle to return false")
	}

	var before, after int
	var ok bool
	runOnMainThread(func() {
		before, after, ok = probeHighRefreshRateToggle()
	})
	if before == -1 {
		t.Skip("WebKit PreferPageRenderingUpdatesNear60FPSEnabled feature not exposed on this macOS version")
	}
	if before != 1 {
		t.Fatalf("expected 60fps cap enabled by default, got %d", before)
	}
	if !ok {
		t.Fatal("expected 60fps cap disable helper to succeed")
	}
	if after != 0 {
		t.Fatalf("expected 60fps cap disabled after toggle, got %d", after)
	}
}
