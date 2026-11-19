package main

import "testing"

func TestLoginPathMergesWithoutDuplicates(t *testing.T) {
	got := loginPath("motd line\n/opt/homebrew/bin:/usr/bin", "/usr/bin:/bin")
	if got != "" {
		t.Fatalf("multi-line shell output must be rejected, got %q", got)
	}
	got = loginPath("/opt/homebrew/bin:/usr/bin", "/usr/bin:/bin")
	if want := "/opt/homebrew/bin:/usr/bin:/bin"; got != want {
		t.Fatalf("loginPath = %q, want %q", got, want)
	}
	if loginPath("  ", "/bin") != "" {
		t.Fatal("empty login PATH must be ignored")
	}
}
