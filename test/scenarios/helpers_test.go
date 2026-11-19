package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/app"
	"github.com/abcdlsj/sumi/internal/computer"
	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
	"github.com/abcdlsj/sumi/internal/server"
	"github.com/abcdlsj/sumi/internal/store"
)

type scenarioEnv struct {
	t         *testing.T
	store     *store.Store
	svc       *app.Service
	srv       *server.Server
	httpSrv   *httptest.Server
	daemon    *computer.Daemon
	space     domain.Space
	owner     domain.Member
	comp      domain.Computer
	compToken string
	binPath   string
	cancel    context.CancelFunc
}

func buildSumiBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sumi")
	cmd := exec.Command("go", "build", "-o", bin, "../../cmd/sumi")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build sumi binary: %v\n%s", err, string(out))
	}
	return bin
}

func startScenarioEnv(t *testing.T, factory func(spec protocol.AssignedAgentSpec, agentHome string) (driver.SessionDriver, error)) *scenarioEnv {
	t.Helper()
	binPath := buildSumiBinary(t)

	dbPath := filepath.Join(t.TempDir(), "scenario.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc := app.NewService(st, app.NewEventHub(), nil)
	srv := server.New(svc, server.Config{}, nil)
	ts := httptest.NewServer(srv.Handler())

	ctx, cancel := context.WithCancel(context.Background())
	auth, err := svc.Register(ctx, "owner@scenario.test", "password123", "Owner")
	if err != nil {
		t.Fatalf("register owner: %v", err)
	}
	space, owner, err := svc.CreateSpace(ctx, auth.User, "scenario", "Scenario Space")
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	comp, token, err := svc.PairComputerDirect(ctx, owner, "test-node", "test.local", "macos")
	if err != nil {
		t.Fatalf("pair computer: %v", err)
	}

	d := computer.NewDaemon(computer.Config{
		ServerURL:     ts.URL,
		ComputerToken: token,
		DataDir:       filepath.Join(t.TempDir(), "daemon"),
		SumiBinary:    binPath,
		DriverFactory: factory,
	}, nil)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	waitUntil(t, 10*time.Second, "computer online", func() bool {
		comps, err := svc.ListComputers(ctx, space.ID)
		return err == nil && len(comps) == 1 && comps[0].ConnectionStatus == "online"
	})

	env := &scenarioEnv{
		t:         t,
		store:     st,
		svc:       svc,
		srv:       srv,
		httpSrv:   ts,
		daemon:    d,
		space:     space,
		owner:     owner,
		comp:      comp,
		compToken: token,
		binPath:   binPath,
		cancel:    cancel,
	}
	t.Cleanup(func() {
		d.Stop()
		cancel()
		ts.Close()
		_ = st.Close()
	})
	return env
}

func waitUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", desc)
}

type cliResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error string          `json:"error"`
}

func runSumiCLI(ctx context.Context, binPath string, in driver.TurnInput, args ...string) (cliResponse, int, error) {
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Dir = in.WorkDir
	cmd.Env = in.Env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return cliResponse{}, -1, err
		}
	}
	var parsed cliResponse
	if jerr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &parsed); jerr != nil {
		return cliResponse{}, exitCode, fmt.Errorf("decode cli json (exit=%d stderr=%s): %w", exitCode, stderr.String(), jerr)
	}
	return parsed, exitCode, nil
}

// funcDriver adapts a Go function into a SessionDriver for deterministic end-to-end scenario testing.
type funcDriver struct {
	fn func(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error)
}

func (f *funcDriver) RunTurn(ctx context.Context, in driver.TurnInput) (driver.TurnResult, error) {
	return f.fn(ctx, in)
}
func (f *funcDriver) ResetSession() error { return nil }
func (f *funcDriver) Close() error        { return nil }

var _ = os.Getenv
var _ = http.StatusOK

var scenarioBuiltinConfig = domain.DriverConfig{BaseURL: "http://127.0.0.1:1/v1", Model: "test-model"}
