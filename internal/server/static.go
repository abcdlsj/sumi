package server

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// SPAHandler serves a built Web UI and falls back to index.html so client-side
// routes survive a reload. API and WebSocket paths are never rewritten.
func SPAHandler(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" {
			if info, err := fs.Stat(assets, name); err == nil && !info.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, assets, "index.html")
	})
}

// DirSPAHandler is SPAHandler over an on-disk build directory.
func DirSPAHandler(root string) http.Handler {
	return SPAHandler(os.DirFS(root))
}
