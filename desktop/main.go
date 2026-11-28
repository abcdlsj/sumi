package main

import (
	"context"
	_ "embed"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/abcdlsj/sumi/internal/cli"
	"github.com/abcdlsj/sumi/internal/lifecycle"
	"github.com/abcdlsj/sumi/internal/version"
)

//go:embed assets/appicon.png
var appIconPNG []byte

//go:embed assets/tray_template.png
var trayTemplatePNG []byte

func main() {
	// Service commands run headless (used when this binary is staged at ~/.sumi/releases/.../sumi
	// to host detached server, computer, agent-host, version, status, and upgrade processes).
	if code, handled := lifecycle.DispatchServiceCommand(os.Args[1:]); handled {
		os.Exit(code)
	}
	// When invoked through the agent PATH symlink (<data_dir>/bin/sumi), run the Agent CLI.
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
	// Boot or hot-upgrade detached dependencies before first paint so the window opens straight onto the Server origin.
	if err := rt.ensureServer(bootCtx); err != nil {
		logger.Error("Cannot start local server", "error", err.Error())
	}
	rt.startDaemon(bootCtx)

	var (
		app              *application.App
		window           *application.WebviewWindow
		statusItem       *application.MenuItem
		ctrl             *controlServer
		ensureConfigured func()
		revealer         *windowRevealer
		pairMu           sync.Mutex
		pairCancel       context.CancelFunc
	)

	stopPairing := func() {
		pairMu.Lock()
		cancel := pairCancel
		pairCancel = nil
		pairMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}

	refreshStatus := func() {
		if statusItem == nil {
			return
		}
		label := "Computer: " + rt.status()
		application.InvokeAsync(func() { statusItem.SetLabel(label) })
	}

	buildStartURL := func(serverURL string, ticket *pairingTicket) string {
		if ticket != nil {
			return ctrl.attachBridge(pairURL(serverURL, *ticket))
		}
		return ctrl.attachBridge(serverURL + "/?shell=desktop")
	}

	// preparePairing creates an initial pairing ticket for serverURL when unpaired
	// and returns the pairer + initial ticket to run in the background.
	preparePairing := func(serverURL string) (*pairer, *pairingTicket) {
		pr := newPairer(serverURL, logger)
		createCtx, cancel := context.WithTimeout(bootCtx, 10*time.Second)
		t, err := pr.client.create(createCtx, pr.hostname, pr.osName)
		cancel()
		var ticket *pairingTicket
		if err != nil {
			logger.Warn("Cannot create pairing", "error", err.Error())
		} else {
			ticket = &t
		}
		rt.setStatus("waiting for confirmation")
		return pr, ticket
	}

	startPairingLoop := func(serverURL string, pr *pairer, initial *pairingTicket) {
		stopPairing()
		ctx, cancel := context.WithCancel(bootCtx)
		pairMu.Lock()
		pairCancel = cancel
		pairMu.Unlock()

		pr.onURL = func(u string) {
			if window != nil {
				target := ctrl.attachBridge(u)
				application.InvokeAsync(func() { window.SetURL(target) })
			}
		}
		go func() {
			secret, err := pr.run(ctx, initial)
			if err != nil {
				return
			}
			next := rt.setComputerToken(serverURL, secret)
			if err := writeConfigAtomic(configPath(), next); err != nil {
				logger.Error("Cannot write desktop config", "error", err.Error())
				rt.setStatus("error")
				refreshStatus()
				return
			}
			rt.startDaemon(bootCtx)
			refreshStatus()
		}()
	}

	onSwitch := func(ctx context.Context, rawURL string) (desktopServerState, error) {
		nextCfg, err := rt.switchServer(ctx, rawURL)
		if err != nil {
			return desktopServerState{}, err
		}
		stopPairing()
		if err := writeConfigAtomic(configPath(), nextCfg); err != nil {
			logger.Error("Cannot write desktop config", "error", err.Error())
		}
		var nextURL string
		if rt.hasComputerToken() {
			rt.startDaemon(bootCtx)
			nextURL = buildStartURL(nextCfg.ServerURL, nil)
		} else {
			pr, ticket := preparePairing(nextCfg.ServerURL)
			nextURL = buildStartURL(nextCfg.ServerURL, ticket)
			startPairingLoop(nextCfg.ServerURL, pr, ticket)
		}
		refreshStatus()
		state := ctrl.currentState()
		// Defer navigation slightly so the HTTP response flushes before the WebView unloads.
		go func() {
			time.Sleep(50 * time.Millisecond)
			if window != nil {
				application.InvokeAsync(func() { window.SetURL(nextURL) })
			}
		}()
		return state, nil
	}

	onOpenURL := func(rawURL string) error {
		if app == nil {
			return nil
		}
		return app.Browser.OpenURL(rawURL)
	}

	presentWindow := func(followUpDelay time.Duration) {
		showAndActivateWindow(window, ensureConfigured, followUpDelay)
	}
	revealer = newWindowRevealer(func() {
		presentWindow(120 * time.Millisecond)
	})

	onHideWindow := func() error {
		if window == nil {
			return nil
		}
		window.Hide()
		return nil
	}

	onRevealWindow := func() error {
		if window == nil {
			return nil
		}
		revealer.reveal()
		return nil
	}

	updCtx, updCancel := context.WithCancel(bootCtx)
	upd := newUpdaterManager(updaterConfig{
		CurrentVersion: version.Value(),
		DataDir:        cfg.DataDir,
		Logger:         logger,
		EnableApply:    true,
		OnQuit: func() {
			stopPairing()
			ctrl.shutdown()
			// Detach cleanly so background server, computer daemon, and agents stay alive across UI updates;
			// the relaunched Sumi.app seamlessly hands off server (WAL flush) and hot-upgrades computer.
			rt.detach()
			if app != nil {
				application.InvokeAsync(func() { app.Quit() })
			}
		},
	})

	ctrl, err = startControlServer(rt, onSwitch, onOpenURL, onHideWindow, onRevealWindow, upd, logger)
	if err != nil {
		logger.Warn("Desktop control server unavailable", "error", err.Error())
	}

	var initPairer *pairer
	var initTicket *pairingTicket
	if !rt.hasComputerToken() {
		initPairer, initTicket = preparePairing(cfg.ServerURL)
	}
	startURL := buildStartURL(cfg.ServerURL, initTicket)

	app = application.New(application.Options{
		Name:        "Sumi",
		Description: "Sumi collaboration workspace",
		Icon:        appIconPNG,
		Logger:      logger,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       "main",
		Title:                      "Sumi",
		Width:                      1360,
		Height:                     900,
		MinWidth:                   900,
		MinHeight:                  600,
		Hidden:                     true,
		BackgroundColour:           application.NewRGBA(244, 244, 247, 255),
		URL:                        startURL,
		JS:                         desktopWindowDragJS,
		DefaultContextMenuDisabled: true,
		// Hidden titlebar (no NSToolbar) keeps native macOS window corners compact instead of
		// Tahoe's wide unified-toolbar corners; refresh_darwin.go centers the traffic lights on
		// the 52px pane-header axis while CSS --wails-draggable drives drag.
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHidden,
		},
	})
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	ensureConfigured = attachNativeWebViewConfig(app, window, logger)
	installApplicationMenu(app, window)

	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		// Safety fallback ensures the window never stays hidden if the remote/local Web UI
		// is slow or predates the /desktop/reveal-window endpoint.
		revealer.scheduleFallback(450 * time.Millisecond)
		upd.start(updCtx)
	})
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) {
		if !revealer.reveal() {
			presentWindow(100 * time.Millisecond)
		}
	})

	tray := app.SystemTray.New()
	tray.SetTemplateIcon(trayTemplatePNG)
	tray.SetTooltip("Sumi")

	menu := app.NewMenu()
	menu.Add("Open Sumi").OnClick(func(*application.Context) {
		if !revealer.reveal() {
			presentWindow(80 * time.Millisecond)
		}
	})
	statusItem = menu.Add("Computer: " + rt.status())
	statusItem.SetEnabled(false)
	menu.Add("Restart Computer Daemon").OnClick(func(*application.Context) {
		rt.restartDaemon(bootCtx)
		statusItem.SetLabel("Computer: " + rt.status())
	})
	menu.Add("Check for Updates…").OnClick(func(*application.Context) {
		go func() {
			st := upd.checkManual(updCtx)
			if st.Available {
				if !revealer.reveal() {
					presentWindow(80 * time.Millisecond)
				}
			}
		}()
	})
	menu.AddSeparator()
	menu.Add("Quit Sumi (Keep Agents Running)").OnClick(func(*application.Context) {
		stopPairing()
		ctrl.shutdown()
		rt.detach()
		app.Quit()
	})
	menu.Add("Stop Local Services & Quit").OnClick(func(*application.Context) {
		stopPairing()
		ctrl.shutdown()
		rt.stopAll()
		app.Quit()
	})
	tray.SetMenu(menu)

	if initPairer != nil {
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
			startPairingLoop(cfg.ServerURL, initPairer, initTicket)
		})
	}

	code := 0
	if err := app.Run(); err != nil {
		logger.Error("Desktop shell exited with error", "error", err.Error())
		code = 1
	}
	revealer.stop()
	updCancel()
	stopPairing()
	ctrl.shutdown()
	// Default shell exit detaches cleanly so background services and agents stay alive across UI updates.
	rt.detach()
	os.Exit(code)
}
