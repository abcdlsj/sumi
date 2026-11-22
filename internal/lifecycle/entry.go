package lifecycle

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/abcdlsj/sumi/internal/agenthost"
	"github.com/abcdlsj/sumi/internal/datadir"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/version"
	"github.com/abcdlsj/sumi/internal/webui"
)

// DispatchServiceCommand handles daemon/lifecycle subcommands shared by `sumi` and `Sumi.app`.
// It returns (exitCode, true) when args[0] matched a service command.
func DispatchServiceCommand(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "server":
		return RunServer(args[1:]), true
	case "computer":
		return RunComputer(args[1:]), true
	case "agent-host":
		return RunAgentHost(args[1:]), true
	case "version":
		return RunVersion(args[1:]), true
	case "status":
		return RunStatus(args[1:]), true
	case "upgrade":
		return RunUpgrade(args[1:]), true
	default:
		return 0, false
	}
}

func resolveDataRoot(flagRoot string) (string, error) {
	if strings.TrimSpace(flagRoot) != "" {
		return flagRoot, nil
	}
	return datadir.Root()
}

func RunVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	info, err := CurrentBuildInfo()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(info)
		return 0
	}
	fmt.Printf("sumi %s (%s)\n", info.Version, info.BinaryHash[:12])
	return 0
}

func RunServer(args []string) int {
	defaultRoot := datadir.RootOrHome()
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	dataRoot := fs.String("data-root", defaultRoot, "Sumi local state root")
	addr := fs.String("addr", "127.0.0.1:8087", "HTTP listen address")
	dbPath := fs.String("db", "", "SQLite database file path (defaults to <data-root>/sumi.db)")
	webOrigin := fs.String("web-origin", "http://localhost:5173", "Allowed Web UI origin for CORS")
	webDir := fs.String("web-dir", "", "Optional static Web UI directory overriding the embedded UI")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := resolveDataRoot(*dataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot resolve local data directory:", err)
		return 1
	}
	db := *dbPath
	if db == "" {
		db = datadir.DBPath(root)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	staticFS := server.SPAHandler(webui.FS())
	if *webDir != "" {
		staticFS = server.DirSPAHandler(*webDir)
	}

	runner, err := NewServerRunner(ServerRunnerConfig{
		DataRoot:  root,
		Addr:      *addr,
		DBPath:    db,
		WebOrigin: *webOrigin,
		WebDir:    *webDir,
		StaticFS:  staticFS,
		Logger:    logger,
	})
	if err != nil {
		logger.Error("Failed to initialize server", "error", err.Error())
		return 1
	}
	if err := runner.Start(); err != nil {
		logger.Error("Failed to start server", "error", err.Error())
		return 1
	}
	logger.Info("Sumi server started", "addr", *addr, "db", db, "pid", os.Getpid())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		logger.Info("Sumi server shutting down")
		runner.Shutdown()
	case <-runner.Done():
		logger.Info("Sumi server exited after handoff/stop")
	}
	return 0
}

func RunComputer(args []string) int {
	defaultRoot := datadir.RootOrHome()
	fs := flag.NewFlagSet("computer", flag.ContinueOnError)
	dataRoot := fs.String("data-root", defaultRoot, "Sumi local state root")
	serverURL := fs.String("server", "http://127.0.0.1:8087", "Sumi server URL")
	token := fs.String("token", os.Getenv("SUMI_COMPUTER_TOKEN"), "Computer bearer token")
	dataDir := fs.String("data-dir", "", "Daemon local state directory (defaults to <data-root>/computer)")
	useSidecar := fs.Bool("sidecar", true, "Run each agent driver in a detached agent-host sidecar")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *token == "" {
		fmt.Fprintln(os.Stderr, "computer token is required (--token or SUMI_COMPUTER_TOKEN)")
		return 2
	}
	root, err := resolveDataRoot(*dataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot resolve local data directory:", err)
		return 1
	}
	compDir := *dataDir
	if compDir == "" {
		compDir = datadir.ComputerDir(root)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	runner, err := NewComputerRunner(ComputerRunnerConfig{
		DataRoot:      root,
		ServerURL:     *serverURL,
		ComputerToken: *token,
		DataDir:       compDir,
		UseSidecar:    *useSidecar,
		Logger:        logger,
	})
	if err != nil {
		logger.Error("Failed to initialize computer daemon", "error", err.Error())
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runner.Start(ctx); err != nil {
		logger.Error("Failed to start computer daemon", "error", err.Error())
		return 1
	}
	logger.Info("Sumi computer daemon started", "server", *serverURL, "socket", runner.Daemon().SocketPath(), "sidecar", *useSidecar, "pid", os.Getpid())

	select {
	case <-ctx.Done():
		logger.Info("Sumi computer daemon shutting down")
		runner.Shutdown(true)
	case <-runner.Done():
		logger.Info("Sumi computer daemon exited after upgrade/stop")
	}
	return 0
}

func RunAgentHost(args []string) int {
	fs := flag.NewFlagSet("agent-host", flag.ContinueOnError)
	sockPath := fs.String("socket", "", "Unix socket path")
	metaPath := fs.String("meta", "", "Metadata file path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *sockPath == "" {
		fmt.Fprintln(os.Stderr, "--socket is required")
		return 2
	}
	srv := agenthost.NewServer(agenthost.ServerConfig{
		SocketPath:  *sockPath,
		MetaPath:    *metaPath,
		HostVersion: version.Value(),
	})
	if err := srv.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start agent-host:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		srv.Close(true)
	case <-srv.Done():
	}
	return 0
}

func RunStatus(args []string) int {
	defaultRoot := datadir.RootOrHome()
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	dataRoot := fs.String("data-root", defaultRoot, "Sumi local state root")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := resolveDataRoot(*dataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var srvStatus *ServerStatus
	var st ServerStatus
	if err := NewControlClient(datadir.ServerControlSocket(root)).GetJSON(ctx, "/status", &st); err == nil {
		srvStatus = &st
	}

	var cmpStatus *ComputerStatus
	var ct ComputerStatus
	if err := NewControlClient(datadir.ComputerControlSocket(root)).GetJSON(ctx, "/status", &ct); err == nil {
		cmpStatus = &ct
	}

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"data_root": root,
			"server":    srvStatus,
			"computer":  cmpStatus,
		})
		return 0
	}

	if srvStatus != nil {
		fmt.Printf("server:   running pid=%d version=%s hash=%s addr=%s\n", srvStatus.PID, srvStatus.Version, shortHash(srvStatus.BinaryHash), srvStatus.Addr)
	} else {
		fmt.Println("server:   not running")
	}
	if cmpStatus != nil {
		fmt.Printf("computer: running pid=%d version=%s hash=%s connected=%v sidecar=%v agents=%d busy=%d draining=%v\n",
			cmpStatus.PID, cmpStatus.Version, shortHash(cmpStatus.BinaryHash),
			cmpStatus.Daemon.Connected, cmpStatus.Daemon.UseSidecar,
			len(cmpStatus.Daemon.HostedAgents), len(cmpStatus.Daemon.BusyAgents), cmpStatus.Daemon.Draining)
	} else {
		fmt.Println("computer: not running")
	}
	return 0
}

func RunUpgrade(args []string) int {
	defaultRoot := datadir.RootOrHome()
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	dataRoot := fs.String("data-root", defaultRoot, "Sumi local state root")
	binaryPath := fs.String("binary", "", "Path to target sumi binary (defaults to current executable)")
	component := fs.String("component", "all", "Component to upgrade: all|server|computer")
	drain := fs.Bool("drain", false, "Drain active turns and restart agent-host sidecars instead of hot sidecar reattach")
	deadline := fs.Duration("deadline", 10*time.Minute, "Maximum time to wait when --drain is enabled")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := resolveDataRoot(*dataRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	src := *binaryPath
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve executable:", err)
			return 1
		}
		src = exe
	}
	if abs, err := filepath.Abs(src); err == nil {
		src = abs
	}

	ctx, cancel := context.WithTimeout(context.Background(), *deadline+2*time.Minute)
	defer cancel()

	stagedPath, info, err := StageBinary(ctx, root, src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stage target binary:", err)
		return 1
	}
	fmt.Printf("staged:   %s (version=%s hash=%s)\n", stagedPath, info.Version, shortHash(info.BinaryHash))

	comp := strings.ToLower(strings.TrimSpace(*component))
	if comp == "all" || comp == "server" {
		srvClient := NewControlClient(datadir.ServerControlSocket(root))
		var st ServerStatus
		if err := srvClient.GetJSON(ctx, "/status", &st); err != nil {
			fmt.Println("server:   not running (skipped)")
		} else if st.BinaryHash == info.BinaryHash {
			fmt.Printf("server:   already at %s (pid=%d)\n", shortHash(info.BinaryHash), st.PID)
		} else {
			var resp HandoffResponse
			if err := srvClient.PostJSON(ctx, "/handoff", HandoffRequest{TargetBinary: stagedPath}, &resp); err != nil {
				fmt.Fprintln(os.Stderr, "server handoff failed:", err)
				return 1
			}
			fmt.Printf("server:   handed off pid %d -> %d (backup=%s)\n", resp.OldPID, resp.NewPID, resp.BackupPath)
		}
	}

	if comp == "all" || comp == "computer" {
		cmpClient := NewControlClient(datadir.ComputerControlSocket(root))
		var ct ComputerStatus
		if err := cmpClient.GetJSON(ctx, "/status", &ct); err != nil {
			fmt.Println("computer: not running (skipped)")
		} else if ct.BinaryHash == info.BinaryHash && !*drain {
			fmt.Printf("computer: already at %s (pid=%d)\n", shortHash(info.BinaryHash), ct.PID)
		} else {
			mode := "hot"
			if *drain {
				mode = "drain"
			}
			var resp ComputerUpgradeResponse
			if err := cmpClient.PostJSON(ctx, "/upgrade", ComputerUpgradeRequest{
				TargetBinary:    stagedPath,
				Mode:            mode,
				DeadlineSeconds: int(deadline.Seconds()),
			}, &resp); err != nil {
				fmt.Fprintln(os.Stderr, "computer upgrade failed:", err)
				return 1
			}
			fmt.Printf("computer: upgraded pid %d -> %d (mode=%s, agents preserved)\n", resp.OldPID, resp.NewPID, resp.Mode)
		}
	}
	return 0
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
