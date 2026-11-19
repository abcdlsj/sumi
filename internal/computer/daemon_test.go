package computer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/driver"
	"github.com/abcdlsj/sumi/internal/protocol"
)

type countingDriver struct {
	id     int
	closed atomic.Bool
	resets atomic.Int32
}

func (c *countingDriver) RunTurn(context.Context, driver.TurnInput) (driver.TurnResult, error) {
	return driver.TurnResult{}, nil
}
func (c *countingDriver) ResetSession() error { c.resets.Add(1); return nil }
func (c *countingDriver) Close() error        { c.closed.Store(true); return nil }

func builtinSpec(model string) protocol.AssignedAgentSpec {
	return protocol.AssignedAgentSpec{
		MemberID: "agent-1", SpaceID: "space-1", DisplayName: "Lin", DriverKind: domain.DriverBuiltin,
		DriverConfig: domain.DriverConfig{BaseURL: "http://127.0.0.1:1/v1", Model: model, APIKey: "sk-daemon"},
	}
}

func TestReprovisionReplacesDriverAndKeepsWorkspace(t *testing.T) {
	var built []*countingDriver
	d := NewDaemon(Config{
		DataDir: t.TempDir(),
		DriverFactory: func(protocol.AssignedAgentSpec, string) (driver.SessionDriver, error) {
			c := &countingDriver{id: len(built)}
			built = append(built, c)
			return c, nil
		},
	}, nil)

	ag := d.provisionAgent(builtinSpec("m1"))
	memFile := filepath.Join(ag.memoryDir, "MEMORY.md")
	if err := os.WriteFile(memFile, []byte("remember"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := ag.driverToken

	// Same spec (e.g. reconnect) keeps the driver and its session.
	d.provisionAgent(builtinSpec("m1"))
	if len(built) != 1 || built[0].resets.Load() != 0 {
		t.Fatalf("unchanged spec must not rebuild: built=%d", len(built))
	}

	ag2 := d.provisionAgent(builtinSpec("m2"))
	if ag2 != ag || len(built) != 2 {
		t.Fatalf("changed spec must rebuild in place: built=%d", len(built))
	}
	ag.mu.Lock()
	cur, model := ag.drv, ag.spec.DriverConfig.Model
	ag.mu.Unlock()
	if cur != built[1] || model != "m2" {
		t.Fatalf("driver not replaced")
	}
	if built[1].resets.Load() != 1 {
		t.Fatalf("re-provision must reset the runtime session")
	}
	waitFor(t, func() bool { return built[0].closed.Load() })
	if b, _ := os.ReadFile(memFile); string(b) != "remember" {
		t.Fatalf("MEMORY.md must survive re-provision")
	}
	if ag.driverToken != token {
		t.Fatalf("driver token must stay stable")
	}
}

func TestProvisionFailureRecordsCode(t *testing.T) {
	d := NewDaemon(Config{DataDir: t.TempDir()}, nil)
	spec := builtinSpec("")
	ag := d.provisionAgent(spec)
	if ag.drv != nil || ag.failCode != domain.AgentErrDriverConfigInvalid || !strings.Contains(ag.failDetail, "driver_config.model") {
		t.Fatalf("got drv=%v code=%q detail=%q", ag.drv, ag.failCode, ag.failDetail)
	}

	spec = protocol.AssignedAgentSpec{MemberID: "agent-2", DriverKind: domain.DriverACP,
		DriverConfig: domain.DriverConfig{Command: []string{"/nonexistent/acp-bin"}}}
	ag = d.provisionAgent(spec)
	if ag.drv != nil || ag.failCode != domain.AgentErrDriverStartFailed {
		t.Fatalf("missing binary: code=%q detail=%q", ag.failCode, ag.failDetail)
	}
}

func TestFailureOfMapsCodesAndRedactsKey(t *testing.T) {
	spec := builtinSpec("m")
	code, detail := failureOf(driver.Fail(domain.AgentErrModelAuthFailed, errors.New("401 bad key sk-daemon\nmore")), spec)
	if code != domain.AgentErrModelAuthFailed || strings.Contains(detail, "sk-daemon") || strings.Contains(detail, "\n") {
		t.Fatalf("code=%q detail=%q", code, detail)
	}
	if code, _ := failureOf(errors.New("plain"), spec); code != domain.AgentErrTurnFailed {
		t.Fatalf("untagged error -> %q", code)
	}
	_, detail = failureOf(errors.New(strings.Repeat("x", 2000)), spec)
	if len([]rune(detail)) != domain.MaxAgentErrorDetail {
		t.Fatalf("detail not bounded: %d", len(detail))
	}
}

func TestAgentEnvAndCLIInstall(t *testing.T) {
	dataDir := t.TempDir()
	fakeBin := filepath.Join(t.TempDir(), "sumi-real")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(Config{DataDir: dataDir, SumiBinary: fakeBin}, nil)
	if err := d.installCLI(); err != nil {
		t.Fatalf("install cli: %v", err)
	}
	// Refresh on restart must replace an existing link.
	if err := d.installCLI(); err != nil {
		t.Fatalf("reinstall cli: %v", err)
	}
	link := filepath.Join(dataDir, "bin", "sumi")
	if target, err := os.Readlink(link); err != nil || target != mustEval(t, fakeBin) {
		t.Fatalf("link target=%q err=%v", target, err)
	}

	ag := &hostedAgent{homeDir: "/agents/a", workDir: "/agents/a/workspace", driverToken: "tok"}
	userHome := os.Getenv("HOME")
	get := func(env []string, key string) []string {
		var vals []string
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				vals = append(vals, strings.TrimPrefix(kv, key+"="))
			}
		}
		return vals
	}
	builtinEnv := d.agentEnv(ag, domain.DriverBuiltin)
	if h := get(builtinEnv, "HOME"); len(h) != 1 || h[0] != "/agents/a" {
		t.Fatalf("builtin HOME=%v", h)
	}
	acpEnv := d.agentEnv(ag, domain.DriverACP)
	if h := get(acpEnv, "HOME"); len(h) != 1 || h[0] != userHome {
		t.Fatalf("acp HOME=%v want user home %q", h, userHome)
	}
	p := get(acpEnv, "PATH")
	if len(p) != 1 || !strings.HasPrefix(p[0], filepath.Join(dataDir, "bin")+string(os.PathListSeparator)) {
		t.Fatalf("PATH must start with bin dir: %v", p)
	}
	if tok := get(acpEnv, "SUMI_DRIVER_TOKEN"); len(tok) != 1 || tok[0] != "tok" {
		t.Fatalf("driver token env=%v", tok)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met")
}
