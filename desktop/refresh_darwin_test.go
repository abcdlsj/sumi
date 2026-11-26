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

func TestConfigureNativeWindowWebView(t *testing.T) {
	if found, _, _ := configureNativeWindowWebView(nil); found {
		t.Fatal("expected nil window handle to return found=false")
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

func TestDisableNativeWebViewOverscroll(t *testing.T) {
	var res overscrollProbeResult
	runOnMainThread(func() {
		res = probeWebViewOverscrollDisable()
	})
	if res.rubberBandBefore == -1 && res.bounceVBefore == -1 && res.bounceHBefore == -1 {
		t.Skip("WKWebView rubber-banding SPI not exposed on this macOS version")
	}
	if res.applied == 0 {
		t.Fatal("expected at least one overscroll flag to be disabled")
	}
	if res.rubberBandBefore != -1 && res.rubberBandAfter != 0 {
		t.Fatalf("expected _rubberBandingEnabled=0 after disable, got %d", res.rubberBandAfter)
	}
	if res.bounceVBefore != -1 && res.bounceVAfter != 0 {
		t.Fatalf("expected _alwaysBounceVertical=0 after disable, got %d", res.bounceVAfter)
	}
	if res.bounceHBefore != -1 && res.bounceHAfter != 0 {
		t.Fatalf("expected _alwaysBounceHorizontal=0 after disable, got %d", res.bounceHAfter)
	}
}
