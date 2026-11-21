package computer

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abcdlsj/sumi/internal/domain"
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
