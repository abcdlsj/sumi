// Package datadir is the single source of truth for where Sumi keeps local state:
// one root (~/.sumi) holding the database, attachment objects, daemon state and
// the desktop config.
package datadir

import (
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
