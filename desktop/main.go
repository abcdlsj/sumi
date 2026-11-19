package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := loadConfig()
	if err != nil {
		logger.Error("Cannot read desktop config", "error", err.Error())
		os.Exit(1)
	}

	rt := newRuntime(cfg, logger)
	bootCtx := context.Background()
	// Boot dependencies before the first paint so the window opens straight onto the Server origin.
	if err := rt.ensureServer(bootCtx); err != nil {
		logger.Error("Cannot start embedded server", "error", err.Error())
	}
	rt.startDaemon(bootCtx)

	app := application.New(application.Options{
		Name:        "Sumi",
		Description: "Sumi collaboration workspace",
		Logger:      logger,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "Sumi",
		Width:            1360,
		Height:           900,
		MinWidth:         900,
		MinHeight:        600,
		BackgroundColour: application.NewRGBA(244, 242, 236, 255),
		URL:              cfg.ServerURL + "/",
	})
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})

	tray := app.SystemTray.New()
	tray.SetTemplateIcon(icons.SystrayMacTemplate)
	tray.SetTooltip("Sumi")

	menu := app.NewMenu()
	menu.Add("Open Sumi").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
	})
	statusItem := menu.Add("Computer: " + rt.status())
	statusItem.SetEnabled(false)
	menu.Add("Restart Computer Daemon").OnClick(func(*application.Context) {
		rt.restartDaemon(bootCtx)
		statusItem.SetLabel("Computer: " + rt.status())
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) {
		rt.shutdown()
		app.Quit()
	})
	tray.SetMenu(menu)

	code := 0
	if err := app.Run(); err != nil {
		logger.Error("Desktop shell exited with error", "error", err.Error())
		code = 1
	}
	rt.shutdown()
	os.Exit(code)
}
