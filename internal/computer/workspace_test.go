package computer

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
	"github.com/abcdlsj/sumi/internal/protocol"
)

func assertDomainCode(t *testing.T, err error, want domain.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", want)
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("expected domain.Error %s, got %T: %v", want, err, err)
	}
	if de.Code != want {
		t.Fatalf("expected error code %s, got %s (%s)", want, de.Code, de.Message)
	}
}

func TestWorkspaceListAndReadSecurity(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	// Create normal workspace content.
	if err := os.MkdirAll(filepath.Join(root, "src", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# Workspace\nHello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("KEY=redacted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api_token.txt"), []byte("redacted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".config", "settings.json"), []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ssh", "known_hosts"), []byte("host"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 0x02, 0xff}, 0o644); err != nil {
		t.Fatal(err)
	}
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if err := os.WriteFile(filepath.Join(root, "logo.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Outside file referenced by symlink and hard link.
	outsideFile := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outsideFile, filepath.Join(root, "hardlink.txt")); err != nil {
		t.Fatal(err)
	}

	// Default listing hides dotfiles, node_modules, and .ssh; lists symlink safely.
	listing, err := listWorkspaceDir(root, "", false)
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	names := make(map[string]domain.WorkspaceFileNode)
	for _, e := range listing.Entries {
		names[e.Name] = e
	}
	if _, ok := names["src"]; !ok || !names["src"].IsDir {
		t.Fatalf("expected src dir in listing: %+v", listing.Entries)
	}
	if _, ok := names["README.md"]; !ok {
		t.Fatalf("expected README.md in listing")
	}
	if _, ok := names["node_modules"]; ok {
		t.Fatalf("node_modules should be ignored")
	}
	if _, ok := names[".config"]; ok {
		t.Fatalf("dotfiles should be hidden when includeHidden=false")
	}
	if _, ok := names[".ssh"]; ok {
		t.Fatalf(".ssh must never be listed")
	}
	if ln, ok := names["link.txt"]; !ok || !ln.IsSymlink || ln.Size != 0 {
		t.Fatalf("symlink should be listed as symlink with size 0, got %+v", ln)
	}

	// Listing with includeHidden=true shows .config and .env, still hides .ssh and node_modules.
	hiddenListing, err := listWorkspaceDir(root, "", true)
	if err != nil {
		t.Fatalf("list hidden: %v", err)
	}
	hiddenNames := make(map[string]bool)
	for _, e := range hiddenListing.Entries {
		hiddenNames[e.Name] = true
	}
	if !hiddenNames[".config"] {
		t.Fatalf("expected .config when includeHidden=true")
	}
	if hiddenNames[".ssh"] {
		t.Fatalf(".ssh must remain hidden even when includeHidden=true")
	}

	// Reading normal UTF-8 text file succeeds.
	readme, err := readWorkspaceFile(root, "README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if readme.Binary || readme.Encoding != "utf8" || readme.Content != "# Workspace\nHello" {
		t.Fatalf("unexpected readme content: %+v", readme)
	}

	// Reading image returns base64 + mime type.
	img, err := readWorkspaceFile(root, "logo.png")
	if err != nil {
		t.Fatalf("read logo.png: %v", err)
	}
	if img.Binary || img.Encoding != "base64" || img.MimeType != "image/png" || img.Content != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Fatalf("unexpected image response: %+v", img)
	}

	// Reading binary file flags Binary=true with empty content.
	bin, err := readWorkspaceFile(root, "bin.dat")
	if err != nil {
		t.Fatalf("read bin.dat: %v", err)
	}
	if !bin.Binary || bin.Content != "" {
		t.Fatalf("expected binary flag with empty content, got %+v", bin)
	}

	// Security rejections.
	_, err = listWorkspaceDir(root, "../outside", false)
	assertDomainCode(t, err, domain.CodeInvalidArgument)

	_, err = listWorkspaceDir(root, "/etc", false)
	assertDomainCode(t, err, domain.CodeInvalidArgument)

	_, err = listWorkspaceDir(root, ".ssh", true)
	assertDomainCode(t, err, domain.CodePermissionDenied)

	_, err = readWorkspaceFile(root, ".env")
	assertDomainCode(t, err, domain.CodePermissionDenied)

	_, err = readWorkspaceFile(root, "api_token.txt")
	assertDomainCode(t, err, domain.CodePermissionDenied)

	_, err = readWorkspaceFile(root, "link.txt")
	assertDomainCode(t, err, domain.CodePermissionDenied)

	_, err = readWorkspaceFile(root, "hardlink.txt")
	assertDomainCode(t, err, domain.CodePermissionDenied)
}

func TestDaemonWorkspaceAndMemoryScopes(t *testing.T) {
	home := t.TempDir()
	workDir := filepath.Join(home, "workspace")
	memDir := filepath.Join(home, "memory")
	notesDir := filepath.Join(memDir, "notes")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(notesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "driver.token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, "MEMORY.md"), []byte("# Memory\nKey fact"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notesDir, "arch.md"), []byte("Arch note"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "driver.token"), filepath.Join(memDir, "escape-link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(workDir, "main.go"), filepath.Join(memDir, "escape-hardlink.md")); err != nil {
		t.Fatal(err)
	}

	d := NewDaemon(Config{DataDir: t.TempDir()}, nil)
	ch := make(chan protocol.Envelope, 16)
	d.wsSendCh = ch
	d.agents["agent-1"] = &hostedAgent{
		homeDir:   home,
		workDir:   workDir,
		memoryDir: memDir,
	}

	listScope := func(agentID string, scope domain.WorkspaceScope, rel string) *protocol.WorkspaceListResultFrame {
		t.Helper()
		d.handleWorkspaceList(protocol.WorkspaceListFrame{
			RequestID:     "req-list",
			AgentMemberID: agentID,
			Scope:         scope,
			Path:          rel,
		})
		env := <-ch
		if env.Type != protocol.TypeWorkspaceListResult || env.WorkspaceListResult == nil {
			t.Fatalf("unexpected list envelope type %q", env.Type)
		}
		return env.WorkspaceListResult
	}
	readScope := func(agentID string, scope domain.WorkspaceScope, rel string) *protocol.WorkspaceReadResultFrame {
		t.Helper()
		d.handleWorkspaceRead(protocol.WorkspaceReadFrame{
			RequestID:     "req-read",
			AgentMemberID: agentID,
			Scope:         scope,
			Path:          rel,
		})
		env := <-ch
		if env.Type != protocol.TypeWorkspaceReadResult || env.WorkspaceReadResult == nil {
			t.Fatalf("unexpected read envelope type %q", env.Type)
		}
		return env.WorkspaceReadResult
	}

	// Empty scope defaults to workspace.
	wsRes := listScope("agent-1", "", "")
	if wsRes.ErrorCode != "" || wsRes.Listing == nil || len(wsRes.Listing.Entries) != 1 || wsRes.Listing.Entries[0].Name != "main.go" {
		t.Fatalf("default scope should list workspace, got err=%s entries=%d", wsRes.ErrorCode, len(wsRes.Listing.Entries))
	}

	// Explicit memory scope lists MEMORY.md and notes/ without workspace files.
	memRes := listScope("agent-1", domain.ScopeMemory, "")
	if memRes.ErrorCode != "" || memRes.Listing == nil {
		t.Fatalf("memory list failed: %s %s", memRes.ErrorCode, memRes.Error)
	}
	memNames := make(map[string]bool)
	for _, e := range memRes.Listing.Entries {
		memNames[e.Name] = true
	}
	if !memNames["MEMORY.md"] || !memNames["notes"] || memNames["main.go"] {
		t.Fatalf("unexpected memory root entries: %+v", memNames)
	}

	// Reading MEMORY.md and nested notes file succeeds.
	readMem := readScope("agent-1", domain.ScopeMemory, "MEMORY.md")
	if readMem.ErrorCode != "" || readMem.File == nil || readMem.File.Content != "# Memory\nKey fact" {
		t.Fatalf("failed reading MEMORY.md: err=%s", readMem.ErrorCode)
	}
	readNote := readScope("agent-1", domain.ScopeMemory, "notes/arch.md")
	if readNote.ErrorCode != "" || readNote.File == nil || readNote.File.Content != "Arch note" {
		t.Fatalf("failed reading nested memory note: err=%s", readNote.ErrorCode)
	}

	// Path traversal, absolute paths, symlinks, hard links, unknown scopes, and unknown agents are rejected.
	if r := readScope("agent-1", domain.ScopeMemory, "../workspace/main.go"); r.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Fatalf("expected traversal rejection, got %q", r.ErrorCode)
	}
	if r := readScope("agent-1", domain.ScopeMemory, "../driver.token"); r.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Fatalf("expected parent secret traversal rejection, got %q", r.ErrorCode)
	}
	if r := listScope("agent-1", domain.ScopeMemory, "../workspace"); r.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Fatalf("expected list traversal rejection, got %q", r.ErrorCode)
	}
	if r := readScope("agent-1", domain.ScopeMemory, "/etc/passwd"); r.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Fatalf("expected absolute path rejection, got %q", r.ErrorCode)
	}
	if r := readScope("agent-1", domain.ScopeMemory, "escape-link.md"); r.ErrorCode != string(domain.CodePermissionDenied) {
		t.Fatalf("expected symlink rejection, got %q", r.ErrorCode)
	}
	if r := readScope("agent-1", domain.ScopeMemory, "escape-hardlink.md"); r.ErrorCode != string(domain.CodePermissionDenied) {
		t.Fatalf("expected hardlink rejection, got %q", r.ErrorCode)
	}
	if r := listScope("agent-1", domain.WorkspaceScope("home"), ""); r.ErrorCode != string(domain.CodeInvalidArgument) {
		t.Fatalf("expected unsupported scope rejection, got %q", r.ErrorCode)
	}
	if r := listScope("missing-agent", domain.ScopeMemory, ""); r.ErrorCode != string(domain.CodeNotFound) {
		t.Fatalf("expected missing agent not_found, got %q", r.ErrorCode)
	}

	// Missing memory directory reports an empty listing at root.
	d.agents["agent-empty"] = &hostedAgent{
		homeDir:   home,
		workDir:   workDir,
		memoryDir: filepath.Join(home, "no-memory-yet"),
	}
	emptyRes := listScope("agent-empty", domain.ScopeMemory, "")
	if emptyRes.ErrorCode != "" || emptyRes.Listing == nil || len(emptyRes.Listing.Entries) != 0 {
		t.Fatalf("expected empty root listing when memory dir missing, got err=%s", emptyRes.ErrorCode)
	}
}
