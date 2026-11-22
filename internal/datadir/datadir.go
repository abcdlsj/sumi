// Package datadir is the single source of truth for where Sumi keeps local state:
// one root (~/.sumi) holding the database, attachment objects, daemon state,
// staged releases, runtime control sockets, and desktop config.
package datadir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// RootDirName is the directory under the user's home holding all local state.
const RootDirName = ".sumi"

// Root returns the local state root, ~/.sumi.
func Root() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, RootDirName), nil
}

// RootOrHome is Root for callers that cannot propagate an error; it falls back to
// a relative .sumi when the home directory is unknown.
func RootOrHome() string {
	root, err := Root()
	if err != nil {
		return RootDirName
	}
	return root
}

// DBPath is the Server's SQLite file inside root.
func DBPath(root string) string { return filepath.Join(root, "sumi.db") }

// ConfigPath is the desktop shell's config file inside root.
func ConfigPath(root string) string { return filepath.Join(root, "desktop.json") }

// ComputerDir is the Computer daemon's state directory inside root.
func ComputerDir(root string) string { return filepath.Join(root, "computer") }

// RunDir holds live PID files and local control sockets for background services.
func RunDir(root string) string { return filepath.Join(root, "run") }

// ReleasesDir holds immutable versioned binaries; running executables are never overwritten in place.
func ReleasesDir(root string) string { return filepath.Join(root, "releases") }

// BinDir holds the active `sumi` symlink pointing into ReleasesDir.
func BinDir(root string) string { return filepath.Join(root, "bin") }

// BackupsDir holds pre-upgrade SQLite snapshots created before Server handoff.
func BackupsDir(root string) string { return filepath.Join(root, "backups") }

func ServerPIDPath(root string) string   { return filepath.Join(RunDir(root), "server.pid") }
func ComputerPIDPath(root string) string { return filepath.Join(RunDir(root), "computer.pid") }

func ServerControlSocket(root string) string {
	return DeterministicSocketPath(filepath.Join(RunDir(root), "server.ctl.sock"), "sumi-srv", root)
}

func ComputerControlSocket(root string) string {
	return DeterministicSocketPath(filepath.Join(RunDir(root), "computer.ctl.sock"), "sumi-cmp", root)
}

// DeterministicSocketPath returns preferred when it fits macOS's 104-byte sockaddr_un limit,
// otherwise a deterministic hashed path in os.TempDir() so restarts bind the exact same socket.
func DeterministicSocketPath(preferred, prefix, key string) string {
	if len(preferred) <= 96 {
		return preferred
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(os.TempDir(), prefix+"-"+hex.EncodeToString(sum[:6])+".sock")
}
