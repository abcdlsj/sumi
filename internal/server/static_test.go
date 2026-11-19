package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesAssetsAndFallsBackToIndex(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":        {Data: []byte("<html>app</html>")},
		"assets/index.js":   {Data: []byte("console.log(1)")},
		"assets/index.css":  {Data: []byte("body{}")},
		"nested/index.html": {Data: []byte("<html>nested</html>")},
	}

	handler := SPAHandler(assets)
	cases := []struct {
		path     string
		wantCode int
		wantBody string
	}{
		{"/", http.StatusOK, "<html>app</html>"},
		{"/assets/index.js", http.StatusOK, "console.log(1)"},
		{"/studio/inbox", http.StatusOK, "<html>app</html>"},
		{"/deeply/nested/client/route", http.StatusOK, "<html>app</html>"},
		{"/api/v1/health", http.StatusNotFound, ""},
		{"/ws/computer", http.StatusNotFound, ""},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.wantCode {
			t.Fatalf("%s: status %d, want %d", tc.path, rec.Code, tc.wantCode)
		}
		if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
			t.Fatalf("%s: body %q, want %q", tc.path, rec.Body.String(), tc.wantBody)
		}
	}
}

func TestDirSPAHandlerServesOnDiskBuild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>disk</html>"), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	handler := DirSPAHandler(dir)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/client/route", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>disk</html>" {
		t.Fatalf("unexpected fallback response: %d %q", rec.Code, rec.Body.String())
	}
}
