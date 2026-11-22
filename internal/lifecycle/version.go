// Package lifecycle implements Sumi's local process management, immutable binary staging,
// verified server FD handoff, and agent-preserving computer daemon upgrades.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/abcdlsj/sumi/internal/datadir"
)

// Version is the semantic version of this binary; override with -ldflags if needed.
var Version = "2.0.0"

// BuildInfo uniquely identifies a Sumi executable by both semver and binary content hash,
// so even local rebuilds with the same semver attest cleanly during handoff.
type BuildInfo struct {
	Version    string `json:"version"`
	BinaryHash string `json:"binary_hash"`
	Executable string `json:"executable,omitempty"`
}

// CurrentBuildInfo returns Version and the SHA-256 digest of the running executable.
func CurrentBuildInfo() (BuildInfo, error) {
	exe, err := os.Executable()
	if err != nil {
		return BuildInfo{}, fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	hash, err := FileHash(exe)
	if err != nil {
		return BuildInfo{}, err
	}
	return BuildInfo{
		Version:    Version,
		BinaryHash: hash,
		Executable: exe,
	}, nil
}

// FileHash computes the lowercase hex SHA-256 digest of path.
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open binary: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash binary: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ProbeBinary executes `<binaryPath> version --json` to confirm the binary is runnable,
// properly signed on macOS, and matches its on-disk SHA-256 before touching live services.
func ProbeBinary(ctx context.Context, binaryPath string) (BuildInfo, error) {
	if strings.TrimSpace(binaryPath) == "" {
		return BuildInfo{}, fmt.Errorf("target binary path is empty")
	}
	resolved := binaryPath
	if r, err := filepath.EvalSymlinks(binaryPath); err == nil {
		resolved = r
	}
	diskHash, err := FileHash(resolved)
	if err != nil {
		return BuildInfo{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, resolved, "version", "--json")
	out, err := cmd.Output()
	if err != nil {
		return BuildInfo{}, fmt.Errorf("probe candidate binary: %w", err)
	}
	var info BuildInfo
	if err := json.Unmarshal(out, &info); err != nil || info.Version == "" {
		return BuildInfo{}, fmt.Errorf("candidate binary returned invalid version payload")
	}
	if info.BinaryHash != "" && info.BinaryHash != diskHash {
		return BuildInfo{}, fmt.Errorf("candidate binary hash mismatch")
	}
	info.BinaryHash = diskHash
	info.Executable = resolved
	return info, nil
}

// StageBinary validates sourcePath, installs an immutable copy into ~/.sumi/releases/<hash12>/sumi,
// and atomically updates ~/.sumi/bin/sumi so running binaries are never overwritten in place.
func StageBinary(ctx context.Context, dataRoot, sourcePath string) (string, BuildInfo, error) {
	info, err := ProbeBinary(ctx, sourcePath)
	if err != nil {
		return "", BuildInfo{}, err
	}
	releaseDir := filepath.Join(datadir.ReleasesDir(dataRoot), info.BinaryHash[:12])
	targetPath := filepath.Join(releaseDir, "sumi")

	alreadyStaged := false
	if existingHash, err := FileHash(targetPath); err == nil && existingHash == info.BinaryHash {
		alreadyStaged = true
	}
	if !alreadyStaged {
		if err := os.MkdirAll(releaseDir, 0o700); err != nil {
			return "", BuildInfo{}, fmt.Errorf("create release dir: %w", err)
		}
		tmpPath := fmt.Sprintf("%s.%d.tmp", targetPath, os.Getpid())
		if err := copyExecutable(info.Executable, tmpPath); err != nil {
			_ = os.Remove(tmpPath)
			return "", BuildInfo{}, err
		}
		// Ad-hoc sign staged copies on macOS when codesign is available so Taskgated accepts them.
		if runtime.GOOS == "darwin" {
			if cs, err := exec.LookPath("codesign"); err == nil {
				_ = exec.CommandContext(ctx, cs, "--force", "--sign", "-", tmpPath).Run()
			}
		}
		if err := os.Rename(tmpPath, targetPath); err != nil {
			_ = os.Remove(tmpPath)
			return "", BuildInfo{}, fmt.Errorf("commit staged binary: %w", err)
		}
	}

	if err := updateActiveSymlink(dataRoot, targetPath); err != nil {
		return "", BuildInfo{}, err
	}
	// Re-probe the staged path (macOS ad-hoc signing may change the binary hash slightly).
	stagedInfo, err := ProbeBinary(ctx, targetPath)
	if err != nil {
		return "", BuildInfo{}, fmt.Errorf("verify staged binary: %w", err)
	}
	return stagedInfo.Executable, stagedInfo, nil
}

// ActiveBinaryPath returns ~/.sumi/bin/sumi when it resolves to an existing executable.
func ActiveBinaryPath(dataRoot string) string {
	link := filepath.Join(datadir.BinDir(dataRoot), "sumi")
	if resolved, err := filepath.EvalSymlinks(link); err == nil {
		if st, err := os.Stat(resolved); err == nil && !st.IsDir() {
			return resolved
		}
	}
	return ""
}

func updateActiveSymlink(dataRoot, targetPath string) error {
	binDir := datadir.BinDir(dataRoot)
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}
	link := filepath.Join(binDir, "sumi")
	tmp := fmt.Sprintf("%s.%d.tmp", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(targetPath, tmp); err != nil {
		return fmt.Errorf("create active symlink: %w", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("swap active symlink: %w", err)
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source binary: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return fmt.Errorf("create staged binary: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync staged binary: %w", err)
	}
	return out.Close()
}
