//go:build !darwin

package main

import (
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func attachHighRefreshRate(_ *application.App, _ *application.WebviewWindow, _ *slog.Logger) {}
