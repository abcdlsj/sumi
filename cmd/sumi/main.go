package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"

	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/cli"
	"github.com/abcdlsj/sumi/internal/computer"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/store"
	"github.com/abcdlsj/sumi/internal/webui"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sumi <server|computer|pair|inbox|read|send|dm|members|follow|unfollow|mute|task|agent|channel>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		runServer(os.Args[2:])
	case "computer":
		runComputer(os.Args[2:])
	default:
		os.Exit(cli.Run(os.Args[1:], os.Stdout))
	}
}

func runServer(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8087", "HTTP listen address")
	dbPath := fs.String("db", "sumi.db", "SQLite database file path")
	webOrigin := fs.String("web-origin", "http://localhost:5173", "Allowed Web UI origin for CORS")
	webDir := fs.String("web-dir", "", "Optional static Web UI directory overriding the embedded UI")
	_ = fs.Parse(args)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := store.Open(*dbPath)
	if err != nil {
		logger.Error("Failed to open SQLite store", "error", err.Error())
		os.Exit(1)
	}
	defer st.Close()

	staticFS := server.SPAHandler(webui.FS())
	if *webDir != "" {
		staticFS = server.DirSPAHandler(*webDir)
	}
	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := server.New(svc, server.Config{WebOrigin: *webOrigin, StaticFS: staticFS}, logger)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("Sumi server started", "addr", *addr, "db", *dbPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err.Error())
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("Sumi server shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	_ = httpSrv.Shutdown(shutCtx)
}

func runComputer(args []string) {
	fs := flag.NewFlagSet("computer", flag.ExitOnError)
	serverURL := fs.String("server", "http://127.0.0.1:8087", "Sumi server URL")
	token := fs.String("token", os.Getenv("SUMI_COMPUTER_TOKEN"), "Computer bearer token")
	dataDir := fs.String("data-dir", "", "Daemon local state directory")
	_ = fs.Parse(args)

	if *token == "" {
		fmt.Fprintln(os.Stderr, "computer token is required (--token or SUMI_COMPUTER_TOKEN)")
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	d := computer.NewDaemon(computer.Config{
		ServerURL:     *serverURL,
		ComputerToken: *token,
		DataDir:       *dataDir,
	}, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Start(ctx); err != nil {
		logger.Error("Failed to start computer daemon", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("Sumi computer daemon started", "server", *serverURL, "socket", d.SocketPath())
	<-ctx.Done()
	d.Stop()
}
