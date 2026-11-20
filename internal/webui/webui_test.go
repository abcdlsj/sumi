package webui

import "testing"

func TestFSContainsIndexHTML(t *testing.T) {
	f, err := FS().Open("index.html")
	if err != nil {
		t.Fatalf("embedded webui must always contain index.html: %v", err)
	}
	_ = f.Close()
}
