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
)

//go:embed assets/appicon.png
var appIconPNG []byte

//go:embed assets/tray_template.png
var trayTemplatePNG []byte

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

	var (
		window     *application.WebviewWindow
		statusItem *application.MenuItem
		ctrl       *controlServer
		pairMu     sync.Mutex
		pairCancel context.CancelFunc
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

	ctrl, err = startControlServer(rt, onSwitch, logger)
	if err != nil {
		logger.Warn("Desktop control server unavailable", "error", err.Error())
	}

	var initPairer *pairer
	var initTicket *pairingTicket
	if !rt.hasComputerToken() {
		initPairer, initTicket = preparePairing(cfg.ServerURL)
	}
	startURL := buildStartURL(cfg.ServerURL, initTicket)

	app := application.New(application.Options{
		Name:        "Sumi",
		Description: "Sumi collaboration workspace",
		Icon:        appIconPNG,
		Logger:      logger,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
	})

	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
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
	tray.SetTemplateIcon(trayTemplatePNG)
	tray.SetTooltip("Sumi")

	menu := app.NewMenu()
	menu.Add("Open Sumi").OnClick(func(*application.Context) {
		window.Show()
		window.Focus()
	})
	statusItem = menu.Add("Computer: " + rt.status())
	statusItem.SetEnabled(false)
	menu.Add("Restart Computer Daemon").OnClick(func(*application.Context) {
		rt.restartDaemon(bootCtx)
		statusItem.SetLabel("Computer: " + rt.status())
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) {
		stopPairing()
		ctrl.shutdown()
		rt.shutdown()
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
	stopPairing()
	ctrl.shutdown()
	rt.shutdown()
	os.Exit(code)
}
