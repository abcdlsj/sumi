package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var embedded embed.FS

// FS returns the built Web UI copied into assets/dist by `make build` or `make desktop`,
// falling back to a placeholder page so plain `go build` / `go test` always succeeds.
func FS() fs.FS {
	if dist, err := fs.Sub(embedded, "assets/dist"); err == nil {
		if _, statErr := fs.Stat(dist, "index.html"); statErr == nil {
			return dist
		}
	}
	placeholder, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic("embedded webui assets missing: " + err.Error())
	}
	return placeholder
}
