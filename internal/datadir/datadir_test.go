package datadir

import (
	"path/filepath"
	"testing"
)

func TestRootFollowsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, err := Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if want := filepath.Join(home, RootDirName); root != want {
		t.Fatalf("root = %q, want %q", root, want)
	}
}

func TestPathsShareOneRoot(t *testing.T) {
	root := "/tmp/sumi-root"
	cases := map[string]string{
		DBPath(root):      filepath.Join(root, "sumi.db"),
		ConfigPath(root):  filepath.Join(root, "desktop.json"),
		ComputerDir(root): filepath.Join(root, "computer"),
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
	}
}
