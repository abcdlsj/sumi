package main

import (
	"strings"
	"testing"
)

func TestDesktopWindowDragBridgeScript(t *testing.T) {
	for _, want := range []string{
		"__sumiDragBridge",
		"--wails-draggable",
		"wails:drag",
		"wails:drag:doubleclick",
		"event.detail !== 1",
		"messageHandlers.external",
	} {
		if !strings.Contains(desktopWindowDragJS, want) {
			t.Fatalf("desktopWindowDragJS missing %q", want)
		}
	}
}
