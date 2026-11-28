//go:build !darwin

package main

import (
	"log/slog"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func attachNativeWebViewConfig(_ *application.App, _ *application.WebviewWindow, _ *slog.Logger) func() {
	return func() {}
}

func showAndActivateWindow(window *application.WebviewWindow, _ func(), _ time.Duration) {
	if window == nil {
		return
	}
	application.InvokeAsync(func() {
		window.UnMinimise()
		window.Show()
		window.Focus()
	})
}
