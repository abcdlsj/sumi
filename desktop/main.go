package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/wailsapp/wails/v3/pkg/icons"

	"github.com/abcdlsj/sumi/internal/cli"
)

func main() {
	// The daemon exposes this binary to agents as <data_dir>/bin/sumi; run as the Agent CLI then.
	if filepath.Base(os.Args[0]) == "sumi" {
		os.Exit(cli.Run(os.Args[1:], os.Stdout))
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	adoptLoginShellPath()

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

	// shell=desktop tells the Web UI to reserve the hidden title bar strip.
	startURL := cfg.ServerURL + "/?shell=desktop"
	var pr *pairer
	var firstTicket *pairingTicket
	if !rt.hasComputerToken() {
		pr = newPairer(cfg.ServerURL, logger)
		createCtx, cancel := context.WithTimeout(bootCtx, 10*time.Second)
		t, err := pr.client.create(createCtx, pr.hostname, pr.osName)
		cancel()
		if err != nil {
			logger.Warn("Cannot create pairing", "error", err.Error())
		} else {
			firstTicket = &t
			startURL = pairURL(cfg.ServerURL, t)
		}
		rt.setStatus("waiting for confirmation")
	}

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
		BackgroundColour: application.NewRGBA(244, 244, 247, 255),
		URL:              startURL,
		// Content runs under a transparent title bar. The drag strip is kept thin so it
		// never covers header buttons; only the rail reserves room for the traffic lights.
		Mac: application.MacWindow{
			TitleBar:                application.MacTitleBarHidden,
			InvisibleTitleBarHeight: 8,
		},
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
	pairCtx, stopPairing := context.WithCancel(bootCtx)
	menu.Add("Quit").OnClick(func(*application.Context) {
		stopPairing()
		rt.shutdown()
		app.Quit()
	})
	tray.SetMenu(menu)

	if pr != nil {
		// Menu and window updates from the pairing goroutine must run on the main thread.
		refreshStatus := func() {
			label := "Computer: " + rt.status()
			application.InvokeAsync(func() { statusItem.SetLabel(label) })
		}
		pr.onURL = func(u string) { window.SetURL(u) }
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			go func() {
				secret, err := pr.run(pairCtx, firstTicket)
				if err != nil {
					return
				}
				next := Config{ServerURL: cfg.ServerURL, DataDir: cfg.DataDir, ComputerToken: secret}
				if err := writeConfigAtomic(configPath(), next); err != nil {
					logger.Error("Cannot write desktop config", "error", err.Error())
					rt.setStatus("error")
					refreshStatus()
					return
				}
				rt.setComputerToken(secret)
				rt.startDaemon(bootCtx)
				refreshStatus()
			}()
		})
	}

	code := 0
	if err := app.Run(); err != nil {
		logger.Error("Desktop shell exited with error", "error", err.Error())
		code = 1
	}
	stopPairing()
	rt.shutdown()
	os.Exit(code)
}
